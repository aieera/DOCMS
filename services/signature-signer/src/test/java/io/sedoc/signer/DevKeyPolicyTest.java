package io.sedoc.signer;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.nio.file.Path;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

/**
 * R-17: the image bakes a self-signed development keystore (password
 * "changeit") and pointed SIGNER_KEYSTORE_PATH at it by default, so a
 * deployment that forgot to mount a real key signed with the dev key.
 */
class DevKeyPolicyTest {

    @TempDir
    Path tmp;

    @Test
    void refusesTheBakedDevKeyWithoutOptIn() throws Exception {
        Path ks = TestKeystores.selfSigned(tmp, "CN=SeDoc Dev Signer,OU=Signing,O=SeDoc,C=US");
        IllegalStateException e = assertThrows(IllegalStateException.class,
                () -> new SignerService(ks.toString(), TestKeystores.PASSWORD, TestKeystores.ALIAS, "", false));
        assertTrue(e.getMessage().contains("SIGNER_ALLOW_DEV_KEY"), e.getMessage());
    }

    @Test
    void acceptsTheDevKeyWithOptIn() throws Exception {
        Path ks = TestKeystores.selfSigned(tmp, "CN=SeDoc Dev Signer,OU=Signing,O=SeDoc,C=US");
        assertDoesNotThrow(() -> new SignerService(ks.toString(), TestKeystores.PASSWORD, TestKeystores.ALIAS, "", true));
    }

    @Test
    void acceptsARealKeyWithoutOptIn() throws Exception {
        Path ks = TestKeystores.selfSigned(tmp, "CN=Acme Corporate Seal,O=Acme,C=DE");
        assertDoesNotThrow(() -> new SignerService(ks.toString(), TestKeystores.PASSWORD, TestKeystores.ALIAS, "", false));
    }

    @Test
    void theBakedPathIsADevKeyWhateverItHolds() {
        assertTrue(DevKeyPolicy.isDevKey("/app/dev-signer.p12", "CN=Anything"));
        assertTrue(DevKeyPolicy.isDevKey("/app/../app/dev-signer.p12", "CN=Anything"));
        assertTrue(DevKeyPolicy.isDevKey("/mnt/keys/seal.p12", "CN=SeDoc Dev Signer,O=SeDoc"));
        assertFalse(DevKeyPolicy.isDevKey("/mnt/keys/seal.p12", "CN=Acme Corporate Seal,O=Acme"));
    }

    @Test
    void configRequiresKeystoreAndPasswordAndOptInIsExactlyOne() {
        assertThrows(IllegalArgumentException.class, () -> SignerServer.Config.fromEnv(Map.of()));
        assertThrows(IllegalArgumentException.class,
                () -> SignerServer.Config.fromEnv(Map.of("SIGNER_KEYSTORE_PATH", "/k.p12")));
        SignerServer.Config c = SignerServer.Config.fromEnv(Map.of(
                "SIGNER_KEYSTORE_PATH", "/k.p12", "SIGNER_KEYSTORE_PASS", "pw"));
        assertFalse(c.allowDevKey());
        assertEquals(6060, c.port());
        for (String v : new String[] {"true", "yes", "0", " 1"}) {
            assertFalse(SignerServer.Config.fromEnv(Map.of(
                    "SIGNER_KEYSTORE_PATH", "/k.p12", "SIGNER_KEYSTORE_PASS", "pw",
                    "SIGNER_ALLOW_DEV_KEY", v)).allowDevKey(), v);
        }
        assertTrue(SignerServer.Config.fromEnv(Map.of(
                "SIGNER_KEYSTORE_PATH", "/k.p12", "SIGNER_KEYSTORE_PASS", "pw",
                "SIGNER_ALLOW_DEV_KEY", "1")).allowDevKey());
    }
}
