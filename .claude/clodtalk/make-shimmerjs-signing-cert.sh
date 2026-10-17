#!/usr/bin/env bash
# Bootstrap the "shimmerjs" login-keychain code-signing identity.
#
# khudson (Accessibility client) and magicbusd (Input Monitoring client) both
# sign under this identity so their TCC grants survive rebuilds. Login-keychain
# material is per-machine and imperative -- nix cannot mint it -- so this runs
# once by hand.
#
# ORDER: run this BEFORE `darwin-rebuild switch`. Activation codesigns against
# this identity and aborts if it is missing. It is NOT a switch step; it only
# touches your login keychain. Re-running is a no-op if the identity exists.
set -euo pipefail

IDENTITY="shimmerjs"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"

if security find-identity -p codesigning -v | grep -q "\"$IDENTITY\""; then
  echo "identity '$IDENTITY' already present:"
  security find-identity -p codesigning -v | grep "$IDENTITY"
  exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Self-signed cert with the codeSigning EKU. Uses a real openssl from nix: the
# host LibreSSL may not support -addext.
nix shell nixpkgs#openssl --command bash -c "
  set -e
  cd '$tmp'
  openssl req -x509 -newkey rsa:2048 -keyout key.pem -out cert.pem -days 3650 -nodes \\
    -subj '/CN=$IDENTITY' \\
    -addext 'basicConstraints=critical,CA:false' \\
    -addext 'keyUsage=critical,digitalSignature' \\
    -addext 'extendedKeyUsage=critical,codeSigning'
  openssl pkcs12 -export -legacy -macalg sha1 -out id.p12 \
    -inkey key.pem -in cert.pem -passout pass:transfer
"

# Import the key+cert and grant codesign permission to use the key. The p12 is
# written with -legacy (SHA-1 MAC + legacy cipher): OpenSSL 3's default MAC is
# SHA-256, which macOS `security import` cannot verify -- it reports that as
# "MAC verification failed (wrong password?)" even when the password is right.
# The transport password is ephemeral (the p12 lives only in $tmp).
security import "$tmp/id.p12" -k "$KEYCHAIN" -P transfer -T /usr/bin/codesign

# codesign only resolves a self-signed identity once it is TRUSTED for code
# signing: an untrusted one fails with "no identity found" (verified). The GUI
# Certificate Assistant trusts self-signed roots automatically; this does it
# explicitly. Admin domain via sudo keeps it terminal-only (no GUI trust
# dialog) and asks for your sudo password; the private key still lives in your
# login keychain, only the trust anchor is system-wide.
sudo security add-trusted-cert -d -r trustRoot -p codeSign \
  -k /Library/Keychains/System.keychain "$tmp/cert.pem"

# Let codesign use the key without a per-signature GUI prompt. Needs the login
# keychain password (usually your login password); press Enter to skip if the
# keychain has no password. If skipped, the FIRST switch shows one "codesign
# wants to use key ... Always Allow" dialog instead -- click Always Allow once.
read -r -s -p "login keychain password (Enter to skip): " KCPW
echo
if [ -n "$KCPW" ]; then
  security set-key-partition-list -S apple-tool:,apple: -k "$KCPW" "$KEYCHAIN" >/dev/null
fi

echo
if security find-identity -p codesigning -v | grep "$IDENTITY"; then
  echo "OK -- '$IDENTITY' code-signing identity ready."
  echo "Next: darwin-rebuild switch (expect one Accessibility + one Input Monitoring re-prompt)."
else
  echo "identity not found after import -- investigate before switching." >&2
  exit 1
fi

# Fallback, only if a later `darwin-rebuild switch` reports the cert untrusted
# ("unable to build chain" / "not trusted"): trust it for code signing (this
# one DOES pop a keychain auth dialog):
#   security add-trusted-cert -p codeSign -k "$KEYCHAIN" <exported cert>.cer
