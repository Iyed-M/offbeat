package dev.offbeat.storage

/** Names are relative to the granted tree. Implementations must reject collisions. */
interface FixtureStorage {
    fun validate()
    fun createDirectory(path: String)
    fun writeNew(path: String, mime: String, bytes: ByteArray)
    fun read(path: String): ByteArray
    fun renameDirectory(from: String, to: String)
    fun deleteFile(path: String)
    fun deleteTree(path: String)
}

data class FixtureFile(val path: String, val mime: String, val bytes: ByteArray)
data class Publication(val directory: String, val paths: List<String>) {
    val audioPaths: List<String> get() = paths.filter { it.startsWith("$directory/tracks/") }
}
