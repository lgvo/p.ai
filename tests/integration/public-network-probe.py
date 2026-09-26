"""Bounded, authentication-free diagnostics and real public-network assertions."""

import json
import ipaddress
import http.client
import os
import socket
import ssl
import struct
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request


def record(kind, **values):
    print(json.dumps({"probe": kind, **values}, sort_keys=True), flush=True)


def read_exact(sock, size):
    data = b""
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise OSError("truncated DNS reply")
        data += chunk
    return data


DOH_UPSTREAMS = (("1.1.1.1", "cloudflare-dns.com"),
                 ("9.9.9.9", "dns.quad9.net"))


def dns_packet(ident=None):
    ident = os.urandom(2) if ident is None else ident
    question = b"\x07example\x03com\x00\x00\x01\x00\x01"
    return ident, question, ident + struct.pack("!5H", 0x0100, 1, 0, 0, 0) + question


def dns_query(server, transport):
    # Plain DNS is confined to the local loopback resolver. Its actual upstream
    # transport is DoH without a plaintext upstream fallback.
    if server != "127.0.0.1" or transport not in ("udp", "tcp"):
        raise ValueError("only loopback DNS is permitted")
    ident, question, packet = dns_packet()
    kind = socket.SOCK_DGRAM if transport == "udp" else socket.SOCK_STREAM
    with socket.socket(socket.AF_INET, kind) as sock:
        sock.settimeout(5)
        sock.connect((server, 53))
        if transport == "tcp":
            sock.sendall(struct.pack("!H", len(packet)) + packet)
            reply = read_exact(sock, struct.unpack("!H", read_exact(sock, 2))[0])
        else:
            sock.send(packet)
            reply = sock.recv(4096)
    return dns_answers(reply, ident, question)


class BootstrapHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self, address, hostname):
        if (address, hostname) not in DOH_UPSTREAMS:
            raise ValueError("unpinned DoH identity")
        super().__init__(hostname, timeout=5, context=ssl.create_default_context())
        self.bootstrap_address = address

    def connect(self):
        # Literal bootstrap avoids port 53; TLS still verifies the provider's
        # hostname and certificate chain. No proxy or ambient credentials.
        raw = socket.create_connection((self.bootstrap_address, 443), self.timeout)
        try:
            self.sock = self._context.wrap_socket(raw, server_hostname=self.host)
        except BaseException:
            raw.close()
            raise


def doh_answers(status, content_type, reply, ident, question):
    if status != 200 or content_type.split(";", 1)[0].strip().lower() != "application/dns-message":
        raise ValueError(f"invalid DoH response: status={status} type={content_type[:96]!r}")
    if len(reply) > 65535:
        raise ValueError("oversized DoH response")
    return dns_answers(reply, ident, question)


def doh_query(address, hostname):
    # RFC8484 recommends ID zero: HTTPS binds the response to its request.
    ident, question, packet = dns_packet(b"\x00\x00")
    connection = BootstrapHTTPSConnection(address, hostname)
    try:
        connection.request("POST", "/dns-query", body=packet,
                           headers={"Accept": "application/dns-message",
                                    "Content-Type": "application/dns-message"})
        response = connection.getresponse()
        return doh_answers(response.status, response.getheader("Content-Type", ""),
                           response.read(65536), ident, question)
    finally:
        connection.close()


def skip_name(reply, offset):
    while True:
        if offset >= len(reply):
            raise ValueError("truncated DNS name")
        size = reply[offset]
        offset += 1
        if size == 0:
            return offset
        if size & 0xC0 == 0xC0:
            if offset >= len(reply):
                raise ValueError("truncated DNS pointer")
            return offset + 1
        if size > 63 or offset + size > len(reply):
            raise ValueError("invalid DNS label")
        offset += size


def dns_answers(reply, ident, question):
    if len(reply) < 12 or reply[:2] != ident:
        raise ValueError("invalid DNS transaction")
    flags, questions, answers, _, _ = struct.unpack("!5H", reply[2:12])
    if not flags & 0x8000 or flags & 0x020F or questions != 1 or answers < 1:
        raise ValueError("DNS reply has no successful, complete answer")
    if reply[12:12 + len(question)] != question:
        raise ValueError("unexpected DNS question")
    offset = 12 + len(question)
    addresses = []
    for _ in range(answers):
        offset = skip_name(reply, offset)
        if offset + 10 > len(reply):
            raise ValueError("truncated DNS resource header")
        rtype, rclass, _, length = struct.unpack("!HHIH", reply[offset:offset + 10])
        offset += 10
        if offset + length > len(reply):
            raise ValueError("truncated DNS resource data")
        if rtype == 1 and rclass == 1 and length == 4:
            address = ipaddress.IPv4Address(reply[offset:offset + length])
            if not address.is_global:
                raise ValueError("public name resolved to non-public address")
            addresses.append(str(address))
        offset += length
    if not addresses:
        raise ValueError("DNS reply has no public IPv4 answer")
    return sorted(set(addresses))


class VerifiedRedirect(urllib.request.HTTPRedirectHandler):
    def __init__(self, url, target):
        super().__init__()
        self.url = url
        self.target = target
        self.observed = False

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if (self.observed or req.full_url != self.url or code != 302
                or headers.get("Location") != self.target or newurl != self.target):
            raise ValueError("unexpected real redirect")
        self.observed = True
        record("redirect", status=code, location=self.target, tls_verified=True)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def opener(redirect=None):
    # No proxy discovery, netrc, cookies, authorization or ambient credentials.
    handlers = [urllib.request.ProxyHandler({}),
                urllib.request.HTTPSHandler(context=ssl.create_default_context())]
    if redirect is not None:
        handlers.append(redirect)
    return urllib.request.build_opener(*handlers)


def verified_redirect_denial(redirect, error):
    return redirect.observed and isinstance(error, urllib.error.URLError) and isinstance(error.reason, TimeoutError)


def main():
    mode = sys.argv[1]
    if mode not in ("outer", "session"):
        raise SystemExit("unknown diagnostic scope")
    for args in (("/run/current-system/sw/bin/ip", "-j", "-4", "address", "show"),
                 ("/run/current-system/sw/bin/ip", "-j", "-4", "route", "show"),
                 ("/run/current-system/sw/bin/ip", "-j", "-6", "route", "show")):
        result = subprocess.run(args, capture_output=True, timeout=3, check=False)
        record("route", scope=mode, argv=list(args), status=result.returncode,
               output=result.stdout.decode(errors="replace")[:4096])
    with open("/etc/resolv.conf", encoding="utf-8") as source:
        record("resolver-config", scope=mode, output=source.read(4096))
    doh_ok = False
    for address, hostname in DOH_UPSTREAMS:
        try:
            answers = doh_query(address, hostname)
            record("dns-over-https", scope=mode, server=address, hostname=hostname,
                   port=443, success=True, addresses=answers, tls_verified=True)
            doh_ok = True
        except (OSError, ValueError, http.client.HTTPException) as error:
            record("dns-over-https", scope=mode, server=address, hostname=hostname,
                   port=443, success=False, error=type(error).__name__,
                   reason=str(error)[:256])
    local_results = []
    for transport in ("udp", "tcp"):
        try:
            answers = dns_query("127.0.0.1", transport)
            record("loopback-dns", scope=mode, server="127.0.0.1", transport=transport,
                   success=True, addresses=answers)
            local_results.append(True)
        except (OSError, ValueError) as error:
            record("loopback-dns", scope=mode, server="127.0.0.1", transport=transport,
                   success=False, error=type(error).__name__)
            local_results.append(False)
    dns_ok = doh_ok and all(local_results)
    # A numeric TLS target separates DNS failure from absence of an outside
    # HTTPS route. It is diagnostic only, never a replacement for the gates.
    try:
        connection = http.client.HTTPSConnection("1.1.1.1", timeout=5,
                                                 context=ssl.create_default_context())
        try:
            connection.request("HEAD", "/")
            response = connection.getresponse()
            record("numeric-https", scope=mode, success=True,
                   status=response.status, tls_verified=True)
        finally:
            connection.close()
    except (OSError, ValueError, http.client.HTTPException) as error:
        record("numeric-https", scope=mode, success=False, error=type(error).__name__)
    https_ok = False
    try:
        with opener().open("https://example.com/", timeout=8) as response:
            body = response.read(65537)
            https_ok = response.status == 200 and len(body) <= 65536 and b"Example Domain" in body
            record("https", scope=mode, success=https_ok, status=response.status,
                   bytes=len(body), tls_verified=True)
    except (OSError, ValueError, urllib.error.URLError) as error:
        record("https", scope=mode, success=False, error=type(error).__name__,
               reason=str(getattr(error, "reason", error))[:256])
    if mode == "outer":
        if not (dns_ok and https_ok):
            raise SystemExit("outer real DoH/DNS/HTTPS gate failed")
        print("P_PUBLIC_OUTER_DOH_PASS", flush=True)
        return
    # Always gather Nix diagnostics even when DNS or HTTPS fails; a nonzero
    # status is a required gate failure, never an UNVERIFIED success marker.
    nix_ok = False
    try:
        result = subprocess.run(
            ["nix", "--refresh", "store", "prefetch-file", "--json", "--name", "p-public-example.html", "https://example.com/"],
            capture_output=True, timeout=35, check=False,
            env={"HOME": "/home/p", "PATH": os.environ["PATH"],
                 "NIX_CONFIG": "connect-timeout = 5\ndownload-attempts = 1\n"})
        record("nix-fetch", status=result.returncode,
               stdout=result.stdout.decode(errors="replace")[:2048],
               stderr=result.stderr.decode(errors="replace")[:2048])
        if result.returncode == 0:
            payload = json.loads(result.stdout)
            with open(payload["storePath"], "rb") as source:
                body = source.read(65537)
            nix_ok = bool(payload.get("hash")) and b"Example Domain" in body and len(body) <= 65536
    except (OSError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
        record("nix-fetch", success=False, error=type(error).__name__)
    if not (dns_ok and https_ok and nix_ok):
        raise SystemExit("real public DNS/HTTPS/Nix-fetch gate failed")
    print("P_PUBLIC_DOH_PASS", flush=True)
    print("P_PUBLIC_DNS_PASS", flush=True)
    print("P_PUBLIC_HTTPS_PASS", flush=True)
    print("P_PUBLIC_NIX_FETCH_PASS", flush=True)

    # Real public DNS returning a private destination must still be denied.
    # The live gateway listener has already been proved by the host-side step.
    private_name = "10.233.0.1.sslip.io"
    answers = socket.getaddrinfo(private_name, 443, socket.AF_INET, socket.SOCK_STREAM)
    addresses = sorted({answer[4][0] for answer in answers})
    record("public-private-dns", hostname=private_name, addresses=addresses)
    if addresses != ["10.233.0.1"]:
        raise SystemExit("public private-destination DNS fixture unavailable")
    try:
        connection = socket.create_connection((addresses[0], 443), timeout=3)
    except TimeoutError:
        print("P_PUBLIC_REAL_RESOLUTION_NEGATIVE_PASS", flush=True)
    else:
        connection.close()
        raise SystemExit("publicly resolved hostname reached gateway")

    # Verify an actual HTTPS redirect response, then follow its exact target.
    # Service failure is not isolation evidence and must fail this gate.
    target = "http://10.233.0.1:443/"
    url = "https://httpbin.org/redirect-to?" + urllib.parse.urlencode({"url": target})
    redirect = VerifiedRedirect(url, target)
    try:
        with opener(redirect).open(url, timeout=8):
            raise SystemExit("HTTPS redirect reached gateway listener")
    except urllib.error.HTTPError as error:
        body = error.read(2049)
        record("redirect-service", status=error.code, bytes=len(body),
               body=body[:2048].decode(errors="replace"), tls_verified=True)
        raise SystemExit("redirect service failure is not denial evidence")
    except urllib.error.URLError as error:
        if not verified_redirect_denial(redirect, error):
            raise SystemExit("unexpected redirect follow failure")
        print("P_PUBLIC_REAL_REDIRECT_NEGATIVE_PASS", flush=True)


if __name__ == "__main__":
    main()
