#!/usr/bin/env python3
"""Generate authorized synthetic tones with ID3 tags and embedded synthetic artwork."""
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1] / 'app/src/main/assets/fixtures'
root.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory() as work:
    work = Path(work)
    for revision in (1, 2):
        for name, base, color in [('a', 440, (40, 150, 220)), ('b', 660, (230, 110, 40))]:
            ppm = work / 'cover.ppm'
            ppm.write_bytes(b'P6\n64 64\n255\n' + bytes(color if revision == 1 else tuple(255 - c for c in color)) * (64 * 64))
            cover = work / 'cover.jpg'
            subprocess.run(['ffmpeg', '-v', 'error', '-y', '-i', str(ppm), '-frames:v', '1', str(cover)], check=True)
            subprocess.run([
                'ffmpeg', '-v', 'error', '-y', '-f', 'lavfi', '-i',
                f'sine=frequency={base + (revision - 1) * 110}:duration=4:sample_rate=44100',
                '-i', str(cover), '-map', '0:a', '-map', '1:v', '-c:a', 'libmp3lame', '-b:a', '96k',
                '-c:v', 'copy', '-id3v2_version', '3', '-disposition:v', 'attached_pic',
                '-metadata', f'title=Tone {name.upper()} v{revision}', '-metadata', 'artist=Offbeat Synthetic',
                '-metadata', f'album=Storage proof v{revision}', '-metadata', f'track={1 if name == "a" else 2}',
                '-metadata:s:v', 'title=Fixture cover', '-metadata:s:v', 'comment=Cover (front)',
                str(root / f'{name}-v{revision}.mp3'),
            ], check=True)
