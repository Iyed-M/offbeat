package dev.offbeat

import android.content.Intent
import android.media.MediaScannerConnection
import android.net.Uri
import android.os.Bundle
import android.provider.DocumentsContract
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.lifecycleScope
import dev.offbeat.storage.FixturePublisher
import dev.offbeat.storage.Fixtures
import dev.offbeat.storage.SafFixtureStorage
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.util.UUID

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val state = getSharedPreferences("storage-proof", MODE_PRIVATE)
        setContent {
            var tree by remember { mutableStateOf(state.getString("tree", null)) }
            var status by remember { mutableStateOf("Select Music/Offbeat in internal shared storage. Then publish fixtures and open the playlists in your music app.") }
            var last by remember { mutableStateOf(state.getString("last", null) ?: "No fixture set published.") }
            var busy by remember { mutableStateOf(false) }
            fun runWork(work: suspend () -> Unit) {
                busy = true
                lifecycleScope.launch {
                    try { work() } catch (error: Exception) {
                        status = "Incomplete: ${error.message?.take(240) ?: "Check folder access and free space, then retry."}"
                    } finally { busy = false }
                }
            }
            val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
                if (uri != null) runWork {
                    withContext(Dispatchers.IO) {
                        contentResolver.takePersistableUriPermission(uri,
                            Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
                        SafFixtureStorage(this@MainActivity, uri).validate()
                        check(state.edit().putString("tree", uri.toString()).remove("last").commit()) { "Cannot save private folder state." }
                    }
                    tree = uri.toString()
                    last = "No fixture set published in this destination."
                    status = "Folder ready. Publish the test files, then try them in your music app."
                }
            }
            fun publish(revision: Int, failure: Boolean) = runWork {
                val uri = Uri.parse(checkNotNull(tree) { "Choose a folder first." })
                status = "Staging fixture v$revision…"
                val storage = SafFixtureStorage(this@MainActivity, uri)
                val publication = withContext(Dispatchers.IO) {
                    val files = Fixtures.files(revision) { assets.open("fixtures/$it").use { stream -> stream.readBytes() } }
                    FixturePublisher(storage).publish(files, UUID.randomUUID().toString(), failure)
                }
                val description = "v$revision: ${publication.directory}"
                withContext(Dispatchers.IO) {
                    check(state.edit().putString("last", description).commit()) { "Files published, but private result could not be saved." }
                }
                last = description
                status = "Test files saved. Making them discoverable to music apps…"
                MediaScannerConnection.scanFile(this@MainActivity, publication.paths.map(storage::mediaPath).toTypedArray(), null) { _, indexed ->
                    if (indexed == null) runOnUiThread { status = "Files saved. Your music app may need its folder scan or playlist import to find them." }
                }
                status = "Files saved. Open the new playlist in your music app; use its folder scan if needed. Earlier test folders are kept."
            }
            MaterialTheme {
                Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(24.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    Text("Offbeat storage proof", style = MaterialTheme.typography.headlineSmall)
                    Text("Test sounds only. Keep this app open while saving them.")
                    Text(tree?.let { "Shared folder: ${DocumentsContract.getTreeDocumentId(Uri.parse(it)).substringAfter(':')}" } ?: "No destination selected.")
                    Button(onClick = { picker.launch(tree?.let(Uri::parse)) }, enabled = !busy) { Text("Choose shared folder") }
                    Button(onClick = { publish(1, false) }, enabled = !busy && tree != null) { Text("Publish fixture v1") }
                    Button(onClick = { publish(2, true) }, enabled = !busy && tree != null) { Text("Fail replacement after staging audio") }
                    Button(onClick = { publish(2, false) }, enabled = !busy && tree != null) { Text("Publish replacement v2") }
                    Text(last)
                    Text(status)
                    Text("v1 order: A, B, A. v2: B, A, B. Both include Liked Songs, an empty playlist, tags and artwork. Enable airplane mode to check offline playback.")
                }
            }
        }
    }
}
