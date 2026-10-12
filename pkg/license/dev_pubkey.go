package license

// DevPublicKeyPEM is the dev/test signing key embedded at build time.
//
// It is the trust root ONLY when SEDOC_ENVIRONMENT is dev (or unset):
// `cmd/license-gen` signs with the matching deploy/license/dev.priv.pem
// (gitignored) for local work. Every other environment must supply the
// release public key through SEDOC_LICENSE_PUBLIC_KEY (PEM) or
// SEDOC_LICENSE_PUBLIC_KEY_FILE (path), and Init refuses this key there
// even when it is configured explicitly (O-10). Rotating the production
// key is a config change plus a restart, not a rebuild.
const DevPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAwugRyfET5avj6CxRpbwl
M8W57R8ryaNLVTASrhYK5JMvVgdtQQebmnHuP3xcOWAVu4MR4fdYjFPl7AIvzGGk
7a+DZxxVM383a7Yc8q/yngRrOaEu5uuW1fC5lMwFD4ecU9G1Ytscpe1+5a3IyS4a
bSeuIzaAyg96h37s/Lzs5LO5h82wX3Kywl8pQ91xtfK30GQL1geniRHEoBfhLJPo
cQE+NGARWZYuEVt9JsxEJ5qEfWQBXiHLsGBY3KeUa/pijXRWhdt4nt6cELwEIc6E
y/DnckOcqpmajkn69xN6TD0d6zQZOtrzfY90uGeZWnqETrSIn/cqG2CeoJysvKiD
BwIDAQAB
-----END PUBLIC KEY-----`
