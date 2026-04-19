package io.vaultdms.signer;

import io.grpc.Status;
import io.grpc.stub.StreamObserver;
import io.vaultdms.signer.grpc.SignRequest;
import io.vaultdms.signer.grpc.SignResponse;
import io.vaultdms.signer.grpc.SignerServiceGrpc;
import io.vaultdms.signer.grpc.VerifyRequest;
import io.vaultdms.signer.grpc.VerifyResponse;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * DSS-backed SignerService — Wave 12.9 scaffold.
 *
 * Wave 12.9 ships the gRPC plumbing (request validation, error
 * mapping, logging hygiene). Wave 12.9b fills in the DSS
 * calls: build {@code PAdESService}, set {@code SignatureLevel =
 * PAdES_BASELINE_LT}, call {@code getDataToSign} / {@code
 * signDocument}, attach the TSA, embed OCSP / CRL.
 *
 * Until that lands, RPCs return {@code UNIMPLEMENTED} — the Go
 * side's {@code DSSSidecarSigner} shell already knows how to
 * surface that as {@link io.grpc.Status.Code#UNIMPLEMENTED}.
 *
 * Logging hygiene note: {@link SignRequest#getPdfBytes()} is PII
 * and must NEVER reach the log. The handler below logs only the
 * signer name, field name, and level. A Wave 13.4 CI guard
 * (scripts/check-pdf-byte-leak.sh) greps the image for logback
 * patterns without the {@code PdfBytes} / {@code SignedPdfBytes}
 * redaction filter.
 */
public final class SignerService extends SignerServiceGrpc.SignerServiceImplBase {

    private static final Logger LOG = LoggerFactory.getLogger(SignerService.class);

    @Override
    public void sign(SignRequest req, StreamObserver<SignResponse> resp) {
        if (req.getPdfBytes().isEmpty()) {
            resp.onError(Status.INVALID_ARGUMENT.withDescription("pdf_bytes required").asRuntimeException());
            return;
        }
        if (req.getSignerName().isEmpty()) {
            resp.onError(Status.INVALID_ARGUMENT.withDescription("signer_name required").asRuntimeException());
            return;
        }
        LOG.info("sign request level={} mode={} field={} signer={}",
                req.getLevel(), req.getMode(), req.getFieldName(), req.getSignerName());

        // Wave 12.9b fills in the DSS PAdES pipeline here.
        resp.onError(Status.UNIMPLEMENTED
                .withDescription("DSS PAdES pipeline lands in Wave 12.9b; signer framework shipped Wave 12.9")
                .asRuntimeException());
    }

    @Override
    public void verify(VerifyRequest req, StreamObserver<VerifyResponse> resp) {
        if (req.getPdfBytes().isEmpty()) {
            resp.onError(Status.INVALID_ARGUMENT.withDescription("pdf_bytes required").asRuntimeException());
            return;
        }
        LOG.info("verify request size={}", req.getPdfBytes().size());

        resp.onError(Status.UNIMPLEMENTED
                .withDescription("Verify lands with the same Wave 12.9b drop as Sign")
                .asRuntimeException());
    }
}
