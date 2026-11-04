"""Fixture-only parser tests; these make no network requests."""
import importlib.util
import pathlib
import struct
import unittest
import urllib.error
import urllib.request
from unittest import mock

path = pathlib.Path(__file__).parents[1] / "integration" / "public-network-probe.py"
spec = importlib.util.spec_from_file_location("public_probe", path)
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class DNSProofTest(unittest.TestCase):
    ident = b"\x01\x02"
    question = b"\x07example\x03com\x00\x00\x01\x00\x01"

    def response(self, flags=0x8180, address=b"\x5d\xb8\xd8\x22"):
        return (self.ident + struct.pack("!5H", flags, 1, 1, 0, 0)
                + self.question + b"\xc0\x0c" + struct.pack("!HHIH", 1, 1, 60, 4) + address)

    def test_real_shaped_public_answer(self):
        self.assertEqual(probe.dns_answers(self.response(), self.ident, self.question),
                         ["93.184.216.34"])

    def test_invalid_or_non_public_answers_are_not_evidence(self):
        cases = [self.response()[:12], self.response()[:-1],
                 self.response(flags=0x8183), self.response(flags=0x8380),
                 self.response(address=b"\x0a\xe9\x00\x01"),
                 b"\x00\x00" + self.response()[2:],
                 self.response()[:12] + b"\x00" + self.response()[13:]]
        for reply in cases:
            with self.subTest(reply=reply), self.assertRaises(ValueError):
                probe.dns_answers(reply, self.ident, self.question)


class DoHProofTest(DNSProofTest):
    def test_response_requires_dns_mime_success_and_bound(self):
        reply = self.response()
        self.assertEqual(probe.doh_answers(200, "application/dns-message", reply,
                                         self.ident, self.question), ["93.184.216.34"])
        for status, mime, data in ((302, "application/dns-message", reply),
                                   (200, "text/html", reply),
                                   (200, "application/dns-message", b"x" * 65536)):
            with self.subTest(status=status, mime=mime), self.assertRaises(ValueError):
                probe.doh_answers(status, mime, data, self.ident, self.question)

    def test_bootstrap_connects_literal_but_verifies_hostname(self):
        connection = probe.BootstrapHTTPSConnection("1.1.1.1", "cloudflare-dns.com")
        raw = mock.Mock()
        context = mock.Mock()
        connection._context = context
        with mock.patch.object(probe.socket, "create_connection", return_value=raw) as create:
            connection.connect()
        create.assert_called_once_with(("1.1.1.1", 443), 5)
        context.wrap_socket.assert_called_once_with(raw, server_hostname="cloudflare-dns.com")
        connection.close()
        with self.assertRaises(ValueError):
            probe.BootstrapHTTPSConnection("10.233.0.1", "cloudflare-dns.com")

    def test_tls_failure_closes_bootstrap_socket(self):
        connection = probe.BootstrapHTTPSConnection("9.9.9.9", "dns.quad9.net")
        raw = mock.Mock()
        connection._context = mock.Mock()
        connection._context.wrap_socket.side_effect = probe.ssl.SSLCertVerificationError()
        with mock.patch.object(probe.socket, "create_connection", return_value=raw):
            with self.assertRaises(probe.ssl.SSLCertVerificationError):
                connection.connect()
        raw.close.assert_called_once()

    def test_doh_uses_rfc_zero_id_with_full_response_validation(self):
        _, question, packet = probe.dns_packet(b"\x00\x00")
        self.assertEqual(packet[:2], b"\x00\x00")
        self.assertEqual(packet[12:], question)
        connection = mock.Mock()
        response = connection.getresponse.return_value
        response.status = 200
        response.getheader.return_value = "application/dns-message"
        response.read.return_value = b"\x00\x00" + self.response()[2:]
        with mock.patch.object(probe, "BootstrapHTTPSConnection", return_value=connection):
            self.assertEqual(probe.doh_query("1.1.1.1", "cloudflare-dns.com"),
                             ["93.184.216.34"])
        self.assertEqual(connection.request.call_args.kwargs["body"][:2], b"\x00\x00")
        connection.close.assert_called_once()
        with self.assertRaises(ValueError):
            probe.doh_answers(200, "application/dns-message", self.response(),
                              b"\x00\x00", question)

    def test_plain_dns_cannot_leave_loopback(self):
        with mock.patch.object(probe.socket, "socket") as socket:
            with self.assertRaises(ValueError):
                probe.dns_query("1.1.1.1", "udp")
        socket.assert_not_called()


class RedirectProofTest(unittest.TestCase):
    url = "https://public.example/redirect"
    target = "http://10.233.0.1:443/"

    def test_only_verified_redirect_destination_timeout_counts(self):
        redirect = probe.VerifiedRedirect(self.url, self.target)
        timeout = urllib.error.URLError(TimeoutError())
        self.assertFalse(probe.verified_redirect_denial(redirect, timeout))
        request = urllib.request.Request(self.url)
        followed = redirect.redirect_request(request, None, 302, "Found",
                                               {"Location": self.target}, self.target)
        self.assertEqual(followed.full_url, self.target)
        self.assertTrue(probe.verified_redirect_denial(redirect, timeout))
        self.assertFalse(probe.verified_redirect_denial(redirect,
                         urllib.error.URLError(ConnectionRefusedError())))

    def test_wrong_or_repeated_redirect_is_not_evidence(self):
        for code, location in ((301, self.target), (302, "http://10.0.0.2/")):
            redirect = probe.VerifiedRedirect(self.url, self.target)
            with self.subTest(code=code, location=location), self.assertRaises(ValueError):
                redirect.redirect_request(urllib.request.Request(self.url), None, code,
                                          "Found", {"Location": location}, location)
            self.assertFalse(redirect.observed)


if __name__ == "__main__":
    unittest.main()
