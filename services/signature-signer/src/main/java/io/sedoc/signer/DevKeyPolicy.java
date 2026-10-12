package io.sedoc.signer;

import java.nio.file.Paths;
import javax.naming.InvalidNameException;
import javax.naming.ldap.LdapName;
import javax.naming.ldap.Rdn;

/**
 * Refuses the development signing key baked into the image (R-17).
 *
 * <p>The Dockerfile generates a self-signed keystore at
 * {@value #BAKED_KEYSTORE_PATH} (subject CN "{@value #BAKED_SUBJECT_CN}",
 * password "changeit") and points SIGNER_KEYSTORE_PATH at it, so that a
 * local stack can sign without provisioning a key. A deployment that forgot
 * to mount its real keystore would otherwise seal documents with that
 * publicly known key. The key is recognised by its baked path or by its
 * subject CN (so copying the file elsewhere does not slip past), and is
 * only accepted when {@value #OPT_IN_ENV}=1.
 */
final class DevKeyPolicy {

    static final String BAKED_KEYSTORE_PATH = "/app/dev-signer.p12";
    static final String BAKED_SUBJECT_CN = "SeDoc Dev Signer";
    static final String OPT_IN_ENV = "SIGNER_ALLOW_DEV_KEY";
    /** The baked dev keystore's password; never set in the image env. */
    static final String BAKED_KEYSTORE_PASS = "changeit";

    /** Reports whether keystorePath is the keystore baked into the image. */
    static boolean isBakedKeystore(String keystorePath) {
        return keystorePath != null && !keystorePath.isBlank()
                && Paths.get(keystorePath).toAbsolutePath().normalize().toString().equals(BAKED_KEYSTORE_PATH);
    }

    /** Reports whether the keystore at {@code keystorePath} holding {@code subjectDn} is the baked dev key. */
    static boolean isDevKey(String keystorePath, String subjectDn) {
        if (isBakedKeystore(keystorePath)) {
            return true;
        }
        try {
            for (Rdn rdn : new LdapName(subjectDn).getRdns()) {
                if ("CN".equalsIgnoreCase(rdn.getType()) && BAKED_SUBJECT_CN.equals(String.valueOf(rdn.getValue()))) {
                    return true;
                }
            }
        } catch (InvalidNameException | IllegalArgumentException e) {
            return false;
        }
        return false;
    }

    /** Throws unless the key is not the dev key or the operator opted in. */
    static void enforce(String keystorePath, String subjectDn, boolean allowDevKey) {
        if (isDevKey(keystorePath, subjectDn) && !allowDevKey) {
            throw new IllegalStateException("refusing the baked development signing key (" + subjectDn
                    + "): mount a real PKCS#12 keystore at SIGNER_KEYSTORE_PATH, or set "
                    + OPT_IN_ENV + "=1 on a local development stack only");
        }
    }

    private DevKeyPolicy() {}
}
