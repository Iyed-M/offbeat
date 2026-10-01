package dev.offbeat.storage

import android.content.Context
import android.media.MediaScannerConnection
import android.net.Uri
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import java.io.IOException
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/** Opt-in real-provider check: select Music/Offbeat in the app before running. */
@RunWith(AndroidJUnit4::class)
class SafFixturePublicationTest {
    @Test fun publishesUsingPersistedGrantAndFailedReplacementPreservesPlayableContent() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val selected = context.getSharedPreferences("storage-proof", Context.MODE_PRIVATE).getString("tree", null)
        assertNotNull("Open Offbeat and choose an internal Music/Offbeat folder before running device tests.", selected)
        val tree = Uri.parse(selected!!)
        val storage = SafFixtureStorage(context, tree)
        storage.validate()
        val firstId = UUID.randomUUID().toString()
        val failedId = UUID.randomUUID().toString()
        val secondId = UUID.randomUUID().toString()
        val unrelated = "unrelated-${UUID.randomUUID()}.txt"
        val unrelatedBytes = "Unrelated user content must survive".encodeToByteArray()
        storage.writeNew(unrelated, "text/plain", unrelatedBytes)
        var first: Publication? = null
        var second: Publication? = null
        try {
            val v1 = Fixtures.files(1) { context.assets.open("fixtures/$it").use { stream -> stream.readBytes() } }
            first = FixturePublisher(storage).publish(v1, firstId)
            for (file in v1) assertArrayEquals(file.bytes, storage.read("${first.directory}/${file.path}"))
            assertThrows(IOException::class.java) {
                FixturePublisher(storage).publish(Fixtures.files(2) {
                    context.assets.open("fixtures/$it").use { stream -> stream.readBytes() }
                }, failedId, true)
            }
            // Recreate storage from the saved tree, rather than relying on cached document handles.
            val reopened = SafFixtureStorage(context, Uri.parse(context.getSharedPreferences(
                "storage-proof", Context.MODE_PRIVATE).getString("tree", null)!!))
            for (file in v1) assertArrayEquals(file.bytes, reopened.read("${first.directory}/${file.path}"))
            assertThrows(IOException::class.java) { reopened.read(".offbeat-staging-$failedId/tracks/a.mp3") }
            val v2 = Fixtures.files(2) { context.assets.open("fixtures/$it").use { stream -> stream.readBytes() } }
            second = FixturePublisher(reopened).publish(v2, secondId)
            for (file in v2) assertArrayEquals(file.bytes, reopened.read("${second.directory}/${file.path}"))
            assertArrayEquals(unrelatedBytes, reopened.read(unrelated))
            assertEquals(
                "#EXTM3U\n#EXTINF:4,Offbeat Synthetic - Tone B v2  #injected \n../tracks/b.mp3\n" +
                    "#EXTINF:4,Offbeat Synthetic - Tone A v2  #injected \n../tracks/a.mp3\n" +
                    "#EXTINF:4,Offbeat Synthetic - Tone B v2  #injected \n../tracks/b.mp3\n",
                reopened.read("${second.directory}/playlists/Ordered duplicates.m3u8").decodeToString(),
            )
            val scanned = CountDownLatch(second.audioPaths.size)
            val failures = AtomicInteger()
            MediaScannerConnection.scanFile(context, second.audioPaths.map(reopened::mediaPath).toTypedArray(), null) { _, uri ->
                if (uri == null) failures.incrementAndGet()
                scanned.countDown()
            }
            assertTrue("Audio indexing timed out; record this storage limitation.", scanned.await(30, TimeUnit.SECONDS))
            assertEquals("Audio was not indexed; record this storage limitation.", 0, failures.get())
        } finally {
            first?.let { storage.deleteTree(it.directory) }
            second?.let { storage.deleteTree(it.directory) }
            storage.deleteFile(unrelated)
        }
    }
}
