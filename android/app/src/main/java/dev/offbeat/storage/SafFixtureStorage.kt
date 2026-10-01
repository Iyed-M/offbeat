package dev.offbeat.storage

import android.content.Context
import android.net.Uri
import android.os.Environment
import android.provider.DocumentsContract
import android.provider.DocumentsContract.Document
import android.webkit.MimeTypeMap
import java.io.IOException
import java.io.ByteArrayOutputStream
import java.util.UUID

/** Deliberately narrow: the platform primary-volume provider below Music only. */
class SafFixtureStorage(context: Context, private val tree: Uri) : FixtureStorage {
    private val resolver = context.contentResolver
    private val root = DocumentsContract.buildDocumentUriUsingTree(tree, DocumentsContract.getTreeDocumentId(tree))

    override fun validate() {
        require(tree.authority == "com.android.externalstorage.documents") {
            "Choose the device's internal shared storage, not a cloud or third-party provider."
        }
        val id = DocumentsContract.getTreeDocumentId(tree)
        require(id.startsWith("primary:Music/") && id.substringAfter("primary:").split('/').none {
            it.isEmpty() || it == "." || it == ".."
        }) { "Choose a dedicated folder under internal storage / Music, such as Music/Offbeat." }
        require(resolver.persistedUriPermissions.any { it.uri == tree && it.isReadPermission && it.isWritePermission }) {
            "Folder access was revoked. Choose the folder again."
        }
        requireFlags(root, Document.FLAG_DIR_SUPPORTS_CREATE)
        // Probe actual operations on a unique scratch directory; never touch existing output.
        val probe = ".offbeat-probe-${UUID.randomUUID()}"
        var current = probe
        createDirectory(probe)
        try {
            writeNew("$probe/.nomedia", "application/octet-stream", byteArrayOf())
            writeNew("$probe/test.partial", "application/octet-stream", byteArrayOf(11, 22, 33))
            check(read("$probe/test.partial").contentEquals(byteArrayOf(11, 22, 33))) { "Provider changed written bytes." }
            deleteFile("$probe/test.partial")
            renameDirectory(probe, "$probe-renamed")
            current = "$probe-renamed"
            check(read("$current/.nomedia").isEmpty()) { "Directory rename did not preserve contents." }
        } finally {
            deleteTree(current)
        }
    }

    override fun createDirectory(path: String) {
        create(path, Document.MIME_TYPE_DIR)
    }

    override fun writeNew(path: String, mime: String, bytes: ByteArray) {
        val uri = create(path, mime)
        requireFlags(uri, Document.FLAG_SUPPORTS_WRITE or Document.FLAG_SUPPORTS_DELETE)
        resolver.openOutputStream(uri, "wt")?.use { it.write(bytes) }
            ?: throw IOException("Cannot open output. Check destination access and free space.")
    }

    override fun read(path: String): ByteArray = resolver.openInputStream(resolve(path))?.use {
        // Fixtures are tiny. Bound reads so a broken provider cannot exhaust the app.
        val output = ByteArrayOutputStream()
        val buffer = ByteArray(8192)
        while (true) {
            val count = it.read(buffer)
            if (count < 0) break
            check(output.size() + count <= 2 * 1024 * 1024) { "Provider returned an oversized fixture." }
            output.write(buffer, 0, count)
        }
        output.toByteArray()
    } ?: throw IOException("Cannot read destination file.")

    override fun renameDirectory(from: String, to: String) {
        require(from.substringBeforeLast('/', "") == to.substringBeforeLast('/', ""))
        val parent = parent(to)
        check(find(parent, to.substringAfterLast('/')) == null) { "Destination collision; existing content was preserved." }
        val source = resolve(from)
        requireFlags(source, Document.FLAG_SUPPORTS_RENAME or Document.FLAG_SUPPORTS_DELETE)
        val renamed = DocumentsContract.renameDocument(resolver, source, to.substringAfterLast('/'))
            ?: throw IOException("Provider does not support directory rename.")
        check(displayName(renamed) == to.substringAfterLast('/')) { "Provider changed the requested directory name." }
    }

    override fun deleteFile(path: String) {
        check(DocumentsContract.deleteDocument(resolver, resolve(path))) { "Provider could not delete a temporary file." }
    }

    override fun deleteTree(path: String) {
        val uri = find(parent(path), path.substringAfterLast('/')) ?: return
        check(DocumentsContract.deleteDocument(resolver, uri)) { "Temporary cleanup failed; retry folder access." }
    }

    /** Only for the supported primary local provider; never use this path for writing. */
    fun mediaPath(path: String): String {
        segments(path)
        val relative = DocumentsContract.getTreeDocumentId(tree).removePrefix("primary:")
        return "${Environment.getExternalStorageDirectory()}/$relative/$path"
    }

    private fun create(path: String, mime: String): Uri {
        val name = segments(path).last()
        val parent = parent(path)
        check(find(parent, name) == null) { "Destination collision; existing content was preserved." }
        val providerMime = if (mime == Document.MIME_TYPE_DIR) mime else
            MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "")) ?: mime
        val uri = DocumentsContract.createDocument(resolver, parent, providerMime, name)
            ?: throw IOException("Provider could not create a document. Check access and space.")
        check(displayName(uri) == name) { "Provider changed the requested filename." }
        return uri
    }

    private fun segments(path: String): List<String> {
        require(path.length in 1..240 && !path.startsWith('/'))
        return path.split('/').also { parts ->
            require(parts.all { it.isNotEmpty() && it != "." && it != ".." && it.none { char -> char.isISOControl() || char == '\\' } })
        }
    }

    private fun parent(path: String): Uri = segments(path).dropLast(1).fold(root) { uri, name ->
        find(uri, name) ?: throw IOException("Destination directory is missing.")
    }

    private fun resolve(path: String) = find(parent(path), segments(path).last())
        ?: throw IOException("Destination file is missing.")

    private fun find(parent: Uri, name: String): Uri? {
        val children = DocumentsContract.buildChildDocumentsUriUsingTree(tree, DocumentsContract.getDocumentId(parent))
        resolver.query(children, arrayOf(Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME), null, null, null)?.use { cursor ->
            while (cursor.moveToNext()) if (cursor.getString(1) == name) {
                return DocumentsContract.buildDocumentUriUsingTree(tree, cursor.getString(0))
            }
        } ?: throw IOException("Cannot list destination. Choose the folder again.")
        return null
    }

    private fun displayName(uri: Uri): String = resolver.query(uri, arrayOf(Document.COLUMN_DISPLAY_NAME), null, null, null)?.use {
        check(it.moveToFirst()); it.getString(0)
    } ?: throw IOException("Cannot inspect destination document.")

    private fun requireFlags(uri: Uri, required: Int) {
        val flags = resolver.query(uri, arrayOf(Document.COLUMN_FLAGS), null, null, null)?.use {
            check(it.moveToFirst()); it.getInt(0)
        } ?: throw IOException("Cannot inspect provider capabilities.")
        check(flags and required == required) { "Destination lacks required create/write/rename/delete operations." }
    }
}
