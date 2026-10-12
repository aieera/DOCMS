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
        SignerService svc = new SignerService(ks.toString(), TestKeystores.PASSWORD, TestKeystores.ALIAS, "");
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
}
