#!/usr/bin/env python3
"""
fix-opus-artists.py — copy multi-value ARTISTS/ALBUMARTISTS tags from the
source FLAC files in MUSIC_FLAC_DIR to the converted Opus files in MUSIC_OGG_DIR.

Called by deebeets as a post-hook after beets import. Uses mutagen, which is
already available since beets depends on it.

Environment variables set by deebeets:
  DEEBEETS_MUSIC_DIR  — the root music directory (e.g. /music/flac)

The script derives the ogg directory by replacing the last path component:
  /music/flac -> /music/ogg
"""
import os
import sys
from pathlib import Path

try:
    from mutagen.flac import FLAC
    from mutagen.oggopus import OggOpus
except ImportError:
    print("fix-opus-artists: mutagen not available, skipping", file=sys.stderr)
    sys.exit(0)

MULTI_VALUE_TAGS = ["ARTISTS", "ALBUMARTISTS"]

def flac_dir_to_ogg_dir(flac_root: Path) -> Path:
    """Derive the ogg directory from the flac directory."""
    return flac_root.parent / "ogg"

def find_opus_for_flac(flac_path: Path, ogg_root: Path, flac_root: Path) -> Path | None:
    """Given a FLAC path, find the corresponding opus file under ogg_root."""
    try:
        rel = flac_path.relative_to(flac_root)
    except ValueError:
        return None
    # beets may rename the file; try same relative path with .opus extension
    candidate = ogg_root / rel.with_suffix(".opus")
    if candidate.exists():
        return candidate
    return None

def fix_file(flac_path: Path, opus_path: Path) -> bool:
    """Copy multi-value tags from flac_path to opus_path. Returns True if changed."""
    try:
        flac = FLAC(flac_path)
        opus = OggOpus(opus_path)
    except Exception as e:
        print(f"fix-opus-artists: error opening {flac_path} / {opus_path}: {e}", file=sys.stderr)
        return False

    changed = False
    for tag in MULTI_VALUE_TAGS:
        values = flac.tags.get(tag, []) if flac.tags else []
        if not values:
            continue
        existing = opus.tags.get(tag.lower(), []) if opus.tags else []
        if list(existing) != list(values):
            if opus.tags is None:
                opus.add_tags()
            opus[tag.lower()] = values
            changed = True

    if changed:
        try:
            opus.save()
        except Exception as e:
            print(f"fix-opus-artists: error saving {opus_path}: {e}", file=sys.stderr)
            return False

    return changed

def main():
    flac_root_str = os.environ.get("DEEBEETS_MUSIC_DIR", "")
    if not flac_root_str:
        print("fix-opus-artists: DEEBEETS_MUSIC_DIR not set, skipping", file=sys.stderr)
        sys.exit(0)

    flac_root = Path(flac_root_str)
    ogg_root = flac_dir_to_ogg_dir(flac_root)

    if not ogg_root.exists():
        sys.exit(0)

    fixed = 0
    skipped = 0
    for flac_path in flac_root.rglob("*.flac"):
        opus_path = find_opus_for_flac(flac_path, ogg_root, flac_root)
        if opus_path is None:
            skipped += 1
            continue
        if fix_file(flac_path, opus_path):
            fixed += 1

    print(f"fix-opus-artists: fixed {fixed} files, skipped {skipped} unmatched FLACs")

if __name__ == "__main__":
    main()
