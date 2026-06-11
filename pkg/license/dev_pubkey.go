package license

// DevPublicKeyPEM is the dev/test signing key embedded at build time.
//
// In prod, the bundled key would be the BD-ops-held public key minted
// during release. For now the dev key in deploy/license/dev.pub.pem
// is the one source of truth — `cmd/license-gen` signs with the
// matching dev.priv.pem (gitignored) and services verify against this.
//
// Key rotation requires a coordinated release: change this constant,
// rebuild every service, distribute new licenses signed by the new
// private key. ADR 0095 § Open questions discusses an alternative
// remote-fetched key endpoint that would avoid the rebuild step.
const DevPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAwugRyfET5avj6CxRpbwl
M8W57R8ryaNLVTASrhYK5JMvVgdtQQebmnHuP3xcOWAVu4MR4fdYjFPl7AIvzGGk
7a+DZxxVM383a7Yc8q/yngRrOaEu5uuW1fC5lMwFD4ecU9G1Ytscpe1+5a3IyS4a
bSeuIzaAyg96h37s/Lzs5LO5h82wX3Kywl8pQ91xtfK30GQL1geniRHEoBfhLJPo
cQE+NGARWZYuEVt9JsxEJ5qEfWQBXiHLsGBY3KeUa/pijXRWhdt4nt6cELwEIc6E
y/DnckOcqpmajkn69xN6TD0d6zQZOtrzfY90uGeZWnqETrSIn/cqG2CeoJysvKiD
BwIDAQAB
-----END PUBLIC KEY-----`
