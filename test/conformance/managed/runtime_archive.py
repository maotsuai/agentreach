"""Bounded data-only tar validation for the P5 backup/restore runbook.

This module never extracts on the Host. Import into a new empty Guest volume
only after validation, archive checksum verification and destination identity
verification. It intentionally rejects all links and special file types.
"""
import hashlib
import io
import posixpath
import tarfile

MAX_ARCHIVE = 64 << 20
MAX_DATA = 48 << 20
MAX_FILES = 4096


def validate_archive(data):
    if len(data) > MAX_ARCHIVE:
        raise ValueError("archive_budget")
    manifest = {}
    total = 0
    seen = set()
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as archive:
        for member in archive:
            if len(seen) >= MAX_FILES:
                raise ValueError("file_count_budget")
            name = member.name
            normalized = posixpath.normpath(name)
            if (not name or "\x00" in name or "\\" in name or name.startswith("/") or
                    any(part == ".." for part in name.split("/")) or
                    ":" in name.split("/")[0] or normalized in seen or len(name.encode("utf-8")) > 1024):
                raise ValueError("unsafe_or_duplicate_path")
            if normalized == "." and not member.isdir():
                raise ValueError("invalid_archive_root")
            if not member.isdir() and not member.isreg():
                raise ValueError("links_and_special_files_forbidden")
            if member.issparse() or member.linkname or any(k.startswith("GNU.sparse") for k in member.pax_headers):
                raise ValueError("sparse_or_link_metadata_forbidden")
            if any(k not in {"path", "mtime", "atime", "ctime"} for k in member.pax_headers):
                raise ValueError("unsupported_archive_metadata")
            if member.size < 0 or member.size > MAX_DATA:
                raise ValueError("file_size_budget")
            total += member.size
            if total > MAX_DATA:
                raise ValueError("data_budget")
            for parent in list(manifest):
                if normalized.startswith(parent + "/") and manifest[parent]["type"] == "file":
                    raise ValueError("file_used_as_directory")
                if parent.startswith(normalized + "/") and not member.isdir():
                    raise ValueError("directory_replaced_by_file")
            seen.add(normalized)
            if member.isdir():
                if member.size:
                    raise ValueError("directory_data_forbidden")
                manifest[normalized] = {"type": "directory"}
                continue
            f = archive.extractfile(member)
            if f is None:
                raise ValueError("missing_file_data")
            h = hashlib.sha256()
            count = 0
            while True:
                chunk = f.read(65536)
                if not chunk:
                    break
                count += len(chunk)
                if count > member.size:
                    raise ValueError("oversized_file_data")
                h.update(chunk)
            if count != member.size:
                raise ValueError("truncated_file_data")
            manifest[normalized] = {"type": "file", "bytes": count, "sha256": h.hexdigest()}
        end = archive.offset
    # Reject concatenated archives or other unvalidated trailing content.
    if len(data) % 512 or len(data[end:]) < 1024 or data[end:end + 1024] != bytes(1024) or any(data[end:]):
        raise ValueError("nonzero_trailing_archive_data")
    return {"schema": 1, "archive_sha256": hashlib.sha256(data).hexdigest(),
            "archive_bytes": len(data), "data_bytes": total, "entries": manifest}


def normalize_for_guest(data, uid=10001, gid=10001):
    """Explicit data-only restore: new owner and regular private file modes."""
    manifest = validate_archive(data)
    result = io.BytesIO()
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as source:
        with tarfile.open(fileobj=result, mode="w", format=tarfile.USTAR_FORMAT) as target:
            for member in source:
                clean = tarfile.TarInfo(posixpath.normpath(member.name))
                clean.type = tarfile.DIRTYPE if member.isdir() else tarfile.REGTYPE
                clean.uid, clean.gid = uid, gid
                clean.mode = 0o755 if member.isdir() else 0o644
                clean.mtime = 0
                clean.size = member.size
                target.addfile(clean, None if member.isdir() else source.extractfile(member))
    normalized = result.getvalue()
    verified = validate_archive(normalized)
    if manifest["entries"] != verified["entries"]:
        raise ValueError("restore_normalization_changed_data")
    return normalized
