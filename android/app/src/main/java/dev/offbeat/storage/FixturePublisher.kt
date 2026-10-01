package dev.offbeat.storage

import java.io.IOException

class FixturePublisher(private val storage: FixtureStorage) {
    fun publish(files: List<FixtureFile>, id: String, controlledFailure: Boolean = false): Publication {
        storage.validate()
        val staging = ".offbeat-staging-$id"
        val destination = "Offbeat-fixture-$id"
        // Outside the cleanup block: a collision is never treated as owned output.
        storage.createDirectory(staging)
        var current = staging
        try {
            storage.writeNew("$staging/.nomedia", "application/octet-stream", byteArrayOf())
            storage.createDirectory("$staging/tracks")
            storage.createDirectory("$staging/playlists")
            for (file in files) {
                storage.writeNew("$staging/${file.path}", file.mime, file.bytes)
                if (!storage.read("$staging/${file.path}").contentEquals(file.bytes)) {
                    throw IOException("Incomplete fixture write. Check free space and folder access; previous files were preserved.")
                }
                if (controlledFailure && file.path.startsWith("tracks/")) {
                    throw IOException("Controlled replacement failure. Previously published files were preserved.")
                }
            }
            storage.renameDirectory(staging, destination)
            current = destination
            storage.deleteFile("$destination/.nomedia")
            return Publication(destination, files.map { "$destination/${it.path}" })
        } catch (error: Exception) {
            try {
                storage.deleteTree(current)
            } catch (cleanup: Exception) {
                error.addSuppressed(cleanup)
                throw IOException("${error.message} Temporary cleanup failed; hidden staging remains. Restore folder access before retrying.", error)
            }
            throw error
        }
    }
}
