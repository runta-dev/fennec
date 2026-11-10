#!/bin/sh
set -eu

name="Fennec Local Development"
keychain="${FENNEC_DEVELOPMENT_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"
work=$(mktemp -d "${TMPDIR:-/tmp}/fennec-development-identity.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
umask 077

if security find-identity "$keychain" | grep -F "\"$name\"" >/dev/null; then
  printf '%s\n' "$name"
  exit 0
fi

# This certificate is only for repeatable local development signing. Release
# builds still require a Developer ID Application identity.
openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
  -subj "/CN=$name/" \
  -addext 'basicConstraints=critical,CA:false' \
  -addext 'keyUsage=critical,digitalSignature' \
  -addext 'extendedKeyUsage=critical,codeSigning' \
  -keyout "$work/private-key.pem" -out "$work/certificate.pem" \
  >/dev/null 2>&1
openssl pkcs12 -export -legacy -passout pass:fennec-development \
  -inkey "$work/private-key.pem" -in "$work/certificate.pem" \
  -out "$work/identity.p12" >/dev/null 2>&1
security import "$work/identity.p12" -k "$keychain" \
  -P fennec-development -T /usr/bin/codesign >/dev/null

fingerprint=$(openssl x509 -in "$work/certificate.pem" -noout -fingerprint -sha1 | cut -d= -f2 | tr -d :)
security find-identity "$keychain" | grep -F "\"$name\"" >/dev/null
printf '%s\n' "$name ($fingerprint)"
