"""Offline, generated-media compatibility probe for docs/tagging-compatibility.md.

Run: uv run --no-project --with 'mutagen==1.47.0' python scripts/verify_tag_compatibility.py
Requires ffmpeg and ffprobe on PATH. Nothing is published to the managed library.
"""

import base64
import hashlib
import json
import shutil
import struct
import subprocess
import tempfile
import unittest
import zlib
from pathlib import Path

from mutagen.flac import FLAC, Picture
from mutagen.id3 import APIC, ID3, TALB, TDRC, TIT2, TPE1, TPE2, TPOS, TRCK, TXXX
from mutagen.mp4 import MP4, MP4Cover
from mutagen.oggopus import OggOpus
from mutagen.oggvorbis import OggVorbis


FORMATS = {
    "opus": ("libopus", "opus"),
    "mp3": ("libmp3lame", "mp3"),
    "m4a": ("aac", "ipod"),
    "flac": ("flac", "flac"),
    "ogg": ("libvorbis", "ogg"),
    "wav": ("pcm_s16le", "wav"),
    "aac": ("aac", "adts"),
}
ARTISTS = ["First Artist", "Second Artist"]
TITLE = "Fixture Title"
ALBUM = "Fixture Album"
URI = "spotify:track:fixture"


def run(*args):
    return subprocess.run(args, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout


def png():
    # Original 2x2 red PNG with standard-library-only construction.
    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    pixels = b"\0" + b"\xff\0\0" * 2
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 2, 2, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(pixels * 2)) + chunk(b"IEND", b""))


def picture():
    image = Picture()
    image.type = 3
    image.mime = "image/png"
    image.width = image.height = 2
    image.depth = 24
    image.data = png()
    return image


def inspect_picture_block(block):
    # Independent binary layout check: FLAC Picture type, MIME, dimensions,
    # and image payload length are big-endian and length-prefixed.
    position = 0

    def integer():
        nonlocal position
        value = struct.unpack_from(">I", block, position)[0]
        position += 4
        return value

    kind = integer()
    mime_size = integer()
    mime = block[position:position + mime_size]
    position += mime_size
    description_size = integer()
    position += description_size
    width, height, depth, colors = (integer() for _ in range(4))
    image_size = integer()
    data = block[position:position + image_size]
    assert position + image_size == len(block)
    return kind, mime, width, height, depth, colors, data


def probe(path):
    return json.loads(run("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", str(path)))


def pcm_digest(path):
    # Independent decoder comparison: copy/remux must leave decoded samples intact.
    pcm = run("ffmpeg", "-v", "error", "-i", str(path), "-map", "0:a:0", "-f", "s16le", "-acodec", "pcm_s16le", "pipe:1")
    assert pcm, "empty decoded audio"
    return hashlib.sha256(pcm).hexdigest()


def write_tags(path, fmt):
    if fmt in ("opus", "ogg", "flac"):
        tags = {"title": [TITLE], "artist": ARTISTS, "album": [ALBUM],
                "albumartist": ["Album Artist"], "tracknumber": ["2"],
                "discnumber": ["1"], "date": ["2024-05-03"], "spotify_uri": [URI]}
        audio = {"opus": OggOpus, "ogg": OggVorbis, "flac": FLAC}[fmt](path)
        for key, value in tags.items():
            audio[key] = value
        if fmt == "flac":
            audio.add_picture(picture())
        else:
            audio["metadata_block_picture"] = [base64.b64encode(picture().write()).decode("ascii")]
        audio.save()
    elif fmt == "mp3":
        tags = ID3(path)
        for frame in (TIT2(encoding=3, text=[TITLE]), TPE1(encoding=3, text=ARTISTS),
                      TALB(encoding=3, text=[ALBUM]), TPE2(encoding=3, text=["Album Artist"]),
                      TRCK(encoding=3, text=["2"]), TPOS(encoding=3, text=["1"]),
                      TDRC(encoding=3, text=["2024-05-03"]),
                      TXXX(encoding=3, desc="SPOTIFY_URI", text=[URI]),
                      APIC(encoding=3, mime="image/png", type=3, desc="Cover", data=png())):
            tags.add(frame)
        tags.save(path, v2_version=4)
    elif fmt == "m4a":
        audio = MP4(path)
        audio["\xa9nam"] = [TITLE]
        audio["\xa9ART"] = ARTISTS
        audio["\xa9alb"] = [ALBUM]
        audio["aART"] = ["Album Artist"]
        audio["trkn"] = [(2, 0)]
        audio["disk"] = [(1, 0)]
        audio["\xa9day"] = ["2024-05-03"]
        audio["----:com.apple.iTunes:SPOTIFY_URI"] = [URI.encode()]
        audio["covr"] = [MP4Cover(png(), imageformat=MP4Cover.FORMAT_PNG)]
        audio.save()


class Compatibility(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not all(shutil.which(tool) for tool in ("ffmpeg", "ffprobe")):
            raise RuntimeError("ffmpeg and ffprobe are required; do not treat missing tools as a pass")

    def test_format_matrix(self):
        with tempfile.TemporaryDirectory(prefix="offbeat-tag-probe-") as temp:
            for fmt, (encoder, muxer) in FORMATS.items():
                with self.subTest(format=fmt):
                    original = Path(temp) / ("original." + fmt)
                    tagged = Path(temp) / ("tagged." + fmt)
                    run("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
                        "-ac", "1", "-ar", "48000", "-c:a", encoder, "-f", muxer, str(original))
                    shutil.copyfile(original, tagged)
                    before = probe(original)
                    before_digest = pcm_digest(original)
                    if fmt in ("wav", "aac"):
                        # Decision gate: passthrough rather than pretending native art/text support.
                        self.assertEqual(original.read_bytes(), tagged.read_bytes())
                        continue
                    write_tags(tagged, fmt)
                    after = probe(tagged)
                    audio_streams = lambda p: [(s["codec_name"], s.get("sample_rate"), s.get("channels"))
                                               for s in p["streams"] if s["codec_type"] == "audio"]
                    self.assertEqual(audio_streams(before), audio_streams(after))
                    self.assertEqual(before_digest, pcm_digest(tagged))
                    if fmt == "mp3":
                        tags = ID3(tagged)
                        self.assertEqual(tags["TPE1"].text, ARTISTS)
                        self.assertEqual(tags["TXXX:SPOTIFY_URI"].text, [URI])
                        self.assertEqual(tags["APIC:Cover"].data, png())
                        self.assertEqual(tags["TIT2"].text, [TITLE])
                        self.assertEqual(tags["TALB"].text, [ALBUM])
                        self.assertEqual(tags["TPE2"].text, ["Album Artist"])
                        self.assertEqual(str(tags["TRCK"]), "2")
                        self.assertEqual(str(tags["TPOS"]), "1")
                        self.assertEqual(str(tags["TDRC"]), "2024-05-03")
                    elif fmt == "m4a":
                        tags = MP4(tagged)
                        self.assertEqual(tags["\xa9ART"], ARTISTS)
                        self.assertEqual(tags["----:com.apple.iTunes:SPOTIFY_URI"], [URI.encode()])
                        self.assertEqual(bytes(tags["covr"][0]), png())
                        for key, expected in (("\xa9nam", [TITLE]), ("\xa9alb", [ALBUM]),
                                              ("aART", ["Album Artist"]), ("\xa9day", ["2024-05-03"]),
                                              ("trkn", [(2, 0)]), ("disk", [(1, 0)])):
                            self.assertEqual(tags[key], expected)
                    else:
                        tags = {"opus": OggOpus, "ogg": OggVorbis, "flac": FLAC}[fmt](tagged)
                        for key, expected in (("title", [TITLE]), ("artist", ARTISTS),
                                              ("album", [ALBUM]), ("albumartist", ["Album Artist"]),
                                              ("tracknumber", ["2"]), ("discnumber", ["1"]),
                                              ("date", ["2024-05-03"]), ("spotify_uri", [URI])):
                            self.assertEqual(tags[key], expected)
                        if fmt == "flac":
                            self.assertEqual(tags.pictures[0].data, png())
                        else:
                            # This is a Vorbis comment containing serialized FLAC Picture,
                            # not an extra input video stream mapped into the file.
                            encoded = tags["metadata_block_picture"][0]
                            decoded = inspect_picture_block(base64.b64decode(encoded, validate=True))
                            self.assertEqual(decoded, (3, b"image/png", 2, 2, 24, 0, png()))
                            pictures = [s for s in after["streams"] if s["codec_type"] == "video"]
                            self.assertTrue(pictures)
                            self.assertTrue(all(s.get("disposition", {}).get("attached_pic") == 1
                                                for s in pictures))
                    # FFprobe reports Ogg comments on the audio stream, other tags on the format.
                    observed = (next(s for s in after["streams"] if s["codec_type"] == "audio").get("tags", {})
                                if fmt in ("opus", "ogg") else after["format"].get("tags", {}))
                    self.assertEqual(observed["title"], TITLE)
                    self.assertEqual(observed["album"], ALBUM)


if __name__ == "__main__":
    unittest.main()
