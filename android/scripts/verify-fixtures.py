#!/usr/bin/env python3
"""Check the committed playable fixtures with native FFmpeg metadata decoding."""
from pathlib import Path
import json
import subprocess

root = Path(__file__).resolve().parents[1] / 'app/src/main/assets/fixtures'
for revision in (1, 2):
    for name in ('a', 'b'):
        path = root / f'{name}-v{revision}.mp3'
        result = json.loads(subprocess.check_output([
            'ffprobe', '-v', 'error', '-show_format', '-show_streams', '-of', 'json', str(path),
        ]))
        tags = result['format']['tags']
        assert tags['title'] == f'Tone {name.upper()} v{revision}', path
        assert tags['artist'] == 'Offbeat Synthetic', path
        assert tags['album'] == f'Storage proof v{revision}', path
        assert 4 <= float(result['format']['duration']) < 4.2, path
        assert any(s['codec_type'] == 'audio' for s in result['streams']), path
        assert any(s['disposition']['attached_pic'] == 1 for s in result['streams']), path
        subprocess.run(['ffmpeg', '-v', 'error', '-i', str(path), '-f', 'null', '-'], check=True)
print('Four synthetic tagged audio/artwork fixtures verified and decoded.')
