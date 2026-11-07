import io
import tarfile
import unittest
from runtime_archive import normalize_for_guest, validate_archive


def tar(entries):
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode="w") as archive:
        for name, kind, content in entries:
            member = tarfile.TarInfo(name)
            member.type = kind
            if kind == tarfile.REGTYPE:
                member.size = len(content)
                archive.addfile(member, io.BytesIO(content))
            else:
                if kind in [tarfile.SYMTYPE, tarfile.LNKTYPE]:
                    member.linkname = content.decode()
                archive.addfile(member)
    return out.getvalue()


class ArchiveTests(unittest.TestCase):
    def test_regular_data_and_checksum(self):
        data = tar([(".", tarfile.DIRTYPE, b""), ("index.html", tarfile.REGTYPE, b"P5_RESTORED")])
        result = validate_archive(data)
        self.assertEqual(result["entries"]["index.html"]["bytes"], 11)
        self.assertEqual(result["data_bytes"], 11)

    def test_unsafe_members_never_imported(self):
        for entry in [
            ("/escape", tarfile.REGTYPE, b"x"),
            ("a/../escape", tarfile.REGTYPE, b"x"),
            ("C:escape", tarfile.REGTYPE, b"x"),
            ("a\\escape", tarfile.REGTYPE, b"x"),
            ("link", tarfile.SYMTYPE, b"/etc"),
            ("hardlink", tarfile.LNKTYPE, b"index.html"),
            ("device", tarfile.CHRTYPE, b""),
            ("fifo", tarfile.FIFOTYPE, b""),
        ]:
            with self.subTest(entry=entry):
                with self.assertRaises(ValueError):
                    validate_archive(tar([entry]))

    def test_duplicate_prefix_and_trailing_data(self):
        for entries in [
            [("a", tarfile.REGTYPE, b"x"), ("./a", tarfile.REGTYPE, b"y")],
            [("a", tarfile.REGTYPE, b"x"), ("a/b", tarfile.REGTYPE, b"y")],
            [("a/b", tarfile.REGTYPE, b"x"), ("a", tarfile.REGTYPE, b"y")],
        ]:
            with self.assertRaises(ValueError):
                validate_archive(tar(entries))
        with self.assertRaises(ValueError):
            validate_archive(tar([("a", tarfile.REGTYPE, b"x")]) + b"UNVALIDATED")

    def test_extended_attributes_rejected_and_owner_normalized(self):
        out = io.BytesIO()
        with tarfile.open(fileobj=out, mode="w", format=tarfile.PAX_FORMAT) as archive:
            member = tarfile.TarInfo("a")
            member.pax_headers = {"SCHILY.xattr.security.capability": "untrusted"}
            archive.addfile(member)
        with self.assertRaises(ValueError):
            validate_archive(out.getvalue())
        data = tar([(".", tarfile.DIRTYPE, b""), ("file", tarfile.REGTYPE, b"content")])
        normalized = normalize_for_guest(data)
        self.assertEqual(validate_archive(data)["entries"], validate_archive(normalized)["entries"])
        with tarfile.open(fileobj=io.BytesIO(normalized), mode="r:") as archive:
            for member in archive:
                self.assertEqual((member.uid, member.gid), (10001, 10001))


if __name__ == "__main__":
    unittest.main()
