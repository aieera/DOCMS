package io.sedoc.signer;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.google.protobuf.ByteString;
import io.grpc.stub.StreamObserver;
import io.sedoc.signer.grpc.Level;
import io.sedoc.signer.grpc.SignRequest;
import io.sedoc.signer.grpc.SignResponse;
import java.nio.charset.StandardCharsets;
import java.nio.file.Path;
import java.util.Collection;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.bouncycastle.cms.CMSSignedData;
import org.bouncycastle.cms.SignerInformation;
import org.bouncycastle.util.encoders.Hex;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class SignerServiceTest {

    @TempDir
    Path tmp;

    /** Captures a unary gRPC response. */
    static final class Capture implements StreamObserver<SignResponse> {
        SignResponse value;
        Throwable error;
        boolean completed;

        @Override public void onNext(SignResponse v) { value = v; }
        @Override public void onError(Throwable t) { error = t; }
        @Override public void onCompleted() { completed = true; }
    }

    static Capture sign(SignerService svc, byte[] pdf, String signer) {
        Capture c = new Capture();
        svc.sign(SignRequest.newBuilder()
                .setPdfBytes(ByteString.copyFrom(pdf))
                .setSignerName(signer)
                .setReason("unit test")
                .setLevel(Level.LEVEL_BB)
                .build(), c);
        return c;
    }

    /** End to end through DSS: a real PAdES-B-B revision is appended. */
    @Test
    void signsAPdfAtBaselineB() throws Exception {
        Path ks = TestKeystores.selfSigned(tmp, "CN=Unit Test Signer,O=SeDoc Test");
        SignerService svc = new SignerService(ks.toString(), TestKeystores.PASSWORD, TestKeystores.ALIAS, "", false);
        byte[] pdf = TestKeystores.minimalPdf();

        Capture c = sign(svc, pdf, "Alice");
        assertNull(c.error, () -> "sign failed: " + c.error);
        assertTrue(c.completed);
        assertNotNull(c.value);
        assertEquals(Level.LEVEL_BB, c.value.getLevel());
        byte[] out = c.value.getPdfBytes().toByteArray();
        assertTrue(out.length > pdf.length, "signed PDF must grow");
        String text = new String(out, StandardCharsets.ISO_8859_1);
        assertTrue(text.startsWith(new String(pdf, StandardCharsets.ISO_8859_1)), "signing must be an incremental update");
        assertTrue(text.contains("/ByteRange"), "a signature dictionary is appended");
        assertEquals(64, c.value.getFingerprint().length(), "SHA-256 hex fingerprint");

        // A second signer chains onto the first revision.
        Capture second = sign(svc, out, "Bob");
        assertNull(second.error, () -> "second sign failed: " + second.error);
        assertTrue(second.value.getPdfBytes().size() > out.length);
    }

    /**
     * PAdES / CAdES baseline B requires the signing certificate inside
     * SignedData.certificates. With the signer's own chain registered as a
     * trust anchor, DSS's default trust-anchor policy dropped it, so no
     * verifier could find the certificate that made the signature.
     */
    @Test
    void signatureCarriesTheSigningCertificate() throws Exception {
        Path ks = TestKeystores.selfSigned(tmp, "CN=Unit Test Signer,O=SeDoc Test");
        SignerService svc = new SignerService(ks.toString(), TestKeystores.PASSWORD, TestKeystores.ALIAS, "", false);
        Capture c = sign(svc, TestKeystores.minimalPdf(), "Alice");
        assertNull(c.error, () -> "sign failed: " + c.error);

        CMSSignedData cms = new CMSSignedData(lastSignatureContents(c.value.getPdfBytes().toByteArray()));
        Collection<SignerInformation> signers = cms.getSignerInfos().getSigners();
        assertEquals(1, signers.size());
        SignerInformation si = signers.iterator().next();
        assertEquals(1, cms.getCertificates().getMatches(si.getSID()).size(),
                "SignedData.certificates must contain the signing certificate");
    }

    private static final Pattern CONTENTS = Pattern.compile("/Contents\\s*<([0-9A-Fa-f]+)>");

    /** The hex /Contents of the last signature dictionary, minus its zero padding. */
    static byte[] lastSignatureContents(byte[] pdf) {
        Matcher m = CONTENTS.matcher(new String(pdf, StandardCharsets.ISO_8859_1));
        String hex = null;
        while (m.find()) {
            hex = m.group(1);
        }
        assertNotNull(hex, "no /Contents in the signed PDF");
        hex = hex.replaceAll("0+$", "");
        if (hex.length() % 2 == 1) {
            hex = hex + "0";
        }
        return Hex.decode(hex);
    }
}
