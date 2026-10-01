package dev.offbeat.storage

import org.junit.Assert.*
import org.junit.Test
import java.io.IOException

class FixturePublisherTest {
    @Test fun publishesCompleteSetWithRelativeOrderedDuplicatePlaylistEntries() {
        val storage = MemoryStorage()
        val publication = FixturePublisher(storage).publish(fixtures(), "first")
        assertEquals("Offbeat-fixture-first", publication.directory)
        assertEquals(listOf("Offbeat-fixture-first/tracks/a.mp3"), publication.audioPaths)
        assertEquals("#EXTM3U\n#EXTINF:2,Artist - A\n../tracks/a.mp3\n#EXTINF:2,Artist - A\n../tracks/a.mp3\n",
            storage.read("Offbeat-fixture-first/playlists/Ordered.m3u8").decodeToString())
        assertFalse(storage.files.keys.any { it.endsWith(".nomedia") || it.startsWith(".offbeat-") })
    }

    @Test fun controlledFailedReplacementPreservesPreviousAudioAndPlaylists() {
        val storage = MemoryStorage()
        val publisher = FixturePublisher(storage)
        publisher.publish(fixtures(), "first")
        val playlist = storage.read("Offbeat-fixture-first/playlists/Ordered.m3u8").copyOf()
        assertThrows(IOException::class.java) { publisher.publish(fixtures(), "replacement", true) }
        assertArrayEquals(byteArrayOf(1, 2, 3), storage.read("Offbeat-fixture-first/tracks/a.mp3"))
        assertArrayEquals(playlist, storage.read("Offbeat-fixture-first/playlists/Ordered.m3u8"))
        assertFalse(storage.files.keys.any { it.contains("replacement") })
    }

    @Test fun truncatedWriteDoesNotPublishAndKeepsPreviousSetUsable() {
        val storage = MemoryStorage()
        val publisher = FixturePublisher(storage)
        publisher.publish(fixtures(), "first")
        storage.truncateWrites = true
        assertThrows(IOException::class.java) { publisher.publish(fixtures(), "truncated") }
        assertArrayEquals(byteArrayOf(1, 2, 3), storage.read("Offbeat-fixture-first/tracks/a.mp3"))
        assertFalse(storage.files.keys.any { it.contains("truncated") })
    }

    @Test fun failedCleanupIsReportedAndLeavesOnlyHiddenIncompleteContent() {
        val storage = MemoryStorage()
        val publisher = FixturePublisher(storage)
        publisher.publish(fixtures(), "first")
        storage.failCleanup = true
        val error = assertThrows(IOException::class.java) { publisher.publish(fixtures(), "cleanup", true) }
        assertTrue(error.message.orEmpty().contains("cleanup failed"))
        assertArrayEquals(byteArrayOf(1, 2, 3), storage.read("Offbeat-fixture-first/tracks/a.mp3"))
        assertTrue(storage.read(".offbeat-staging-cleanup/.nomedia").isEmpty())
        assertFalse(storage.files.keys.any { it.startsWith("Offbeat-fixture-cleanup/") })
    }

    @Test fun publishesSafeFixturePresentationAndReplacementOrderWithoutChangingOldPlaylists() {
        val storage = MemoryStorage()
        val publisher = FixturePublisher(storage)
        publisher.publish(Fixtures.files(1) { byteArrayOf(1, 2, 3) }, "v1")
        publisher.publish(Fixtures.files(2) { byteArrayOf(4, 5, 6) }, "v2")
        assertEquals(
            "#EXTM3U\n#EXTINF:4,Offbeat Synthetic - Tone A v1  #injected \n../tracks/a.mp3\n" +
                "#EXTINF:4,Offbeat Synthetic - Tone B v1  #injected \n../tracks/b.mp3\n" +
                "#EXTINF:4,Offbeat Synthetic - Tone A v1  #injected \n../tracks/a.mp3\n",
            storage.read("Offbeat-fixture-v1/playlists/Ordered duplicates.m3u8").decodeToString(),
        )
        assertEquals(
            "#EXTM3U\n#EXTINF:4,Offbeat Synthetic - Tone B v2  #injected \n../tracks/b.mp3\n" +
                "#EXTINF:4,Offbeat Synthetic - Tone A v2  #injected \n../tracks/a.mp3\n" +
                "#EXTINF:4,Offbeat Synthetic - Tone B v2  #injected \n../tracks/b.mp3\n",
            storage.read("Offbeat-fixture-v2/playlists/Liked Songs.m3u8").decodeToString(),
        )
        assertEquals("#EXTM3U\n", storage.read("Offbeat-fixture-v2/playlists/Empty.m3u8").decodeToString())
        assertArrayEquals(byteArrayOf(1, 2, 3), storage.read("Offbeat-fixture-v1/tracks/a.mp3"))
        assertArrayEquals(byteArrayOf(4, 5, 6), storage.read("Offbeat-fixture-v2/tracks/a.mp3"))
    }

    private fun fixtures() = listOf(
        FixtureFile("tracks/a.mp3", "audio/mpeg", byteArrayOf(1, 2, 3)),
        FixtureFile("playlists/Ordered.m3u8", "audio/x-mpegurl",
            "#EXTM3U\n#EXTINF:2,Artist - A\n../tracks/a.mp3\n#EXTINF:2,Artist - A\n../tracks/a.mp3\n".encodeToByteArray()),
    )

    private class MemoryStorage : FixtureStorage {
        val files = linkedMapOf<String, ByteArray>()
        val directories = mutableSetOf<String>()
        var truncateWrites = false
        var failCleanup = false
        override fun validate() = Unit
        override fun createDirectory(path: String) { check(directories.add(path)) }
        override fun writeNew(path: String, mime: String, bytes: ByteArray) {
            check(!files.containsKey(path)); files[path] = if (truncateWrites && path.endsWith("a.mp3")) bytes.copyOf(1) else bytes.copyOf()
        }
        override fun read(path: String) = files[path] ?: throw IOException("Missing file")
        override fun renameDirectory(from: String, to: String) {
            check(!directories.contains(to))
            val moved = files.filterKeys { it.startsWith("$from/") }
            moved.forEach { (path, bytes) -> files.remove(path); files[to + path.removePrefix(from)] = bytes }
            val dirs = directories.filter { it == from || it.startsWith("$from/") }
            dirs.forEach { directories.remove(it); directories.add(to + it.removePrefix(from)) }
        }
        override fun deleteFile(path: String) { check(files.remove(path) != null) }
        override fun deleteTree(path: String) {
            if (failCleanup) throw IOException("Provider unavailable")
            files.keys.removeAll { it.startsWith("$path/") }
            directories.removeAll { it == path || it.startsWith("$path/") }
        }
    }
}
