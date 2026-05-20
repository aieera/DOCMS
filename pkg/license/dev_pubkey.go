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
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA26kAUgD/GGbuCWLhfnBq
1wMOZVwtvN8+9DGFvH58ajpyvYbwZt0Yeb1LJOz1uRNWOGm4cn5069rtTPpTFOxj
+V7DHeTr1nrBpAw8j4IHgfDRULXswC0NCWpnZSHrfAn80XYRnCy3q/rLy0OxCaxE
llRf9deC9jC8mZfO6VBfobmbn1U86i9I6p2S/7UnRgdVHPKULQySob7JN/Ln1GPS
43Ss6NEmKXYcRq2AFD6+UcD3wLJhkR7Gc5GFRsf7eoZae3sUTjIqY/ZZLSeeCQUr
2l9igixeTAaayyFKQMlHudJ8Ua41tBwzIUmsQhz9p7a3kiaIiDHPID5JJyiiMtFc
lwIDAQAB
-----END PUBLIC KEY-----`
