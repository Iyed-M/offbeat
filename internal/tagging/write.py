"""Offline Mutagen 1.47.0 text-tag writer. Protocol: one bounded JSON object on stdin.

The path is a private staged copy supplied by the daemon, never a managed file.
No network access or package installation is performed here.
"""

import json
import sys
import base64

try:
    import mutagen
    from mutagen.flac import FLAC, Picture
    from mutagen.id3 import ID3, ID3NoHeaderError, APIC, TALB, TDRC, TIT2, TPE1, TPE2, TPOS, TRCK, TXXX
    from mutagen.mp4 import MP4, MP4Cover
    from mutagen.oggopus import OggOpus
    from mutagen.oggvorbis import OggVorbis
except ImportError:
    sys.exit(2)


def write(path, extension, data, picture=None, mime=None):
    artists = data["artists"]
    if extension in ("opus", "ogg", "flac"):
        audio = {"opus": OggOpus, "ogg": OggVorbis, "flac": FLAC}[extension](path)
        fields = {"title": [data["title"]], "artist": artists,
                  "album": [data["album"]], "spotify_uri": [data["uri"]]}
        for key, value in (("albumartist", data["album_artist"]),
                           ("tracknumber", data["track_number"]),
                           ("discnumber", data["disc_number"]), ("date", data["release_date"])):
            if value:
                fields[key] = [str(value)]
        for key, value in fields.items():
            audio[key] = value
        for key in ("albumartist", "tracknumber", "discnumber", "date"):
            if key not in fields and key in audio:
                del audio[key]
        if picture is not None:
            cover = Picture()
            cover.type = 3
            cover.mime = mime
            cover.data = picture
            if extension == "flac":
                audio.clear_pictures()
                audio.add_picture(cover)
            else:
                audio["metadata_block_picture"] = [base64.b64encode(cover.write()).decode("ascii")]
        audio.save()
    elif extension == "mp3":
        try:
            tags = ID3(path)
        except ID3NoHeaderError:
            tags = ID3()
        frames = [TIT2(encoding=3, text=[data["title"]]), TPE1(encoding=3, text=artists),
                  TALB(encoding=3, text=[data["album"]]),
                  TXXX(encoding=3, desc="SPOTIFY_URI", text=[data["uri"]])]
        for value, frame in ((data["album_artist"], TPE2), (data["track_number"], TRCK),
                             (data["disc_number"], TPOS), (data["release_date"], TDRC)):
            if value:
                frames.append(frame(encoding=3, text=[str(value)]))
        for frame in frames:
            tags.add(frame)
        for value, frame_id in ((data["album_artist"], "TPE2"),
                                (data["track_number"], "TRCK"),
                                (data["disc_number"], "TPOS"),
                                (data["release_date"], "TDRC")):
            if not value:
                tags.delall(frame_id)
        if picture is not None:
            tags.delall("APIC")
            tags.add(APIC(encoding=3, mime=mime, type=3, desc="Cover", data=picture))
        tags.save(path, v2_version=4)
    elif extension == "m4a":
        audio = MP4(path)
        fields = {"\xa9nam": [data["title"]], "\xa9ART": artists,
                  "\xa9alb": [data["album"]],
                  "----:com.apple.iTunes:SPOTIFY_URI": [data["uri"].encode("utf-8")]}
        for key, value in (("aART", data["album_artist"]), ("\xa9day", data["release_date"])):
            if value:
                fields[key] = [value]
        for key, value in (("trkn", data["track_number"]), ("disk", data["disc_number"])):
            if value:
                fields[key] = [(value, 0)]
        for key, value in fields.items():
            audio[key] = value
        for key in ("aART", "trkn", "disk", "\xa9day"):
            if key not in fields and key in audio:
                del audio[key]
        if picture is not None:
            audio["covr"] = [MP4Cover(picture, imageformat=MP4Cover.FORMAT_PNG if mime == "image/png" else MP4Cover.FORMAT_JPEG)]
        audio.save()
    else:
        raise ValueError("unsupported format")

    # Read the staged file back, not the in-memory writer, before Go verifies
    # unchanged decoded audio and publishes the descriptor.
    if picture is not None:
        if extension == "flac":
            saved = FLAC(path)
            covers = [p.data for p in saved.pictures if p.type == 3 and p.mime == mime]
        elif extension in ("opus", "ogg"):
            saved = (OggOpus if extension == "opus" else OggVorbis)(path)
            covers = [Picture(base64.b64decode(value)).data for value in saved.get("metadata_block_picture", [])]
        elif extension == "mp3":
            covers = [p.data for p in ID3(path).getall("APIC") if p.type == 3 and p.mime == mime]
        else:
            covers = [bytes(p) for p in MP4(path).get("covr", [])]
        if picture not in covers:
            raise ValueError("picture readback failed")


if __name__ == "__main__":
    # No traceback: exceptions may contain private filesystem paths or metadata.
    try:
        if mutagen.version_string != "1.47.0":
            sys.exit(3)
        if len(sys.argv) not in (3, 5):
            raise ValueError("invalid arguments")
        payload = sys.stdin.buffer.read(128 * 1024 + 1)
        if len(payload) > 128 * 1024:
            raise ValueError("metadata exceeds limit")
        picture = None
        mime = None
        if len(sys.argv) == 5:
            mime = sys.argv[4]
            if mime not in ("image/png", "image/jpeg"):
                raise ValueError("invalid picture type")
            with open(sys.argv[3], "rb") as file:
                picture = file.read(5 * 1024 * 1024 + 1)
            if not picture or len(picture) > 5 * 1024 * 1024:
                raise ValueError("invalid picture size")
        write(sys.argv[1], sys.argv[2], json.loads(payload), picture, mime)
    except Exception:
        sys.exit(1)
