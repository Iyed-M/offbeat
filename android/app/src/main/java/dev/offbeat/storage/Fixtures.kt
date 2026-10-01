package dev.offbeat.storage

object Fixtures {
    fun files(revision: Int, asset: (String) -> ByteArray): List<FixtureFile> {
        require(revision in 1..2)
        val order = if (revision == 1) listOf("a", "b", "a") else listOf("b", "a", "b")
        val playlist = buildString {
            append("#EXTM3U\n")
            for (name in order) {
                // Deliberately adversarial presentation text exercises EXTINF sanitization.
                val title = "Tone ${name.uppercase()} v$revision\r\n#injected\u0000"
                append("#EXTINF:4,${safeText("Offbeat Synthetic - $title")}\n")
                append("../tracks/$name.mp3\n")
            }
        }.encodeToByteArray()
        return listOf(
            FixtureFile("tracks/a.mp3", "audio/mpeg", asset("a-v$revision.mp3")),
            FixtureFile("tracks/b.mp3", "audio/mpeg", asset("b-v$revision.mp3")),
            FixtureFile("playlists/Ordered duplicates.m3u8", "audio/x-mpegurl", playlist),
            FixtureFile("playlists/Liked Songs.m3u8", "audio/x-mpegurl", playlist),
            FixtureFile("playlists/Empty.m3u8", "audio/x-mpegurl", "#EXTM3U\n".encodeToByteArray()),
        )
    }

    private fun safeText(value: String) = value.map { if (it.isISOControl()) ' ' else it }.joinToString("")
}
