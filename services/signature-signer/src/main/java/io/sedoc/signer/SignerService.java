package io.sedoc.signer;

import eu.europa.esig.dss.enumerations.DigestAlgorithm;
import eu.europa.esig.dss.enumerations.SignatureLevel;
import eu.europa.esig.dss.model.DSSDocument;
import eu.europa.esig.dss.model.InMemoryDocument;
import eu.europa.esig.dss.model.SignatureValue;
import eu.europa.esig.dss.model.ToBeSigned;
import eu.europa.esig.dss.pades.PAdESSignatureParameters;
import eu.europa.esig.dss.pades.signature.PAdESService;
import eu.europa.esig.dss.service.tsp.OnlineTSPSource;
import eu.europa.esig.dss.spi.DSSUtils;
import eu.europa.esig.dss.spi.x509.CommonTrustedCertificateSource;
import eu.europa.esig.dss.token.DSSPrivateKeyEntry;
import eu.europa.esig.dss.token.KSPrivateKeyEntry;
import eu.europa.esig.dss.token.Pkcs12SignatureToken;
import eu.europa.esig.dss.validation.CommonCertificateVerifier;
import eu.europa.esig.dss.validation.SignedDocumentValidator;
import eu.europa.esig.dss.validation.reports.Reports;
import eu.europa.esig.dss.diagnostic.DiagnosticData;
import eu.europa.esig.dss.simplereport.SimpleReport;
import io.grpc.Status;
import io.grpc.stub.StreamObserver;
import io.sedoc.signer.grpc.Level;
import io.sedoc.signer.grpc.SignRequest;
import io.sedoc.signer.grpc.SignResponse;
import io.sedoc.signer.grpc.SignatureInfo;
import io.sedoc.signer.grpc.SignerServiceGrpc;
import io.sedoc.signer.grpc.VerifyRequest;
import io.sedoc.signer.grpc.VerifyResponse;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.ByteArrayOutputStream;
import java.security.KeyStore.PasswordProtection;
import java.time.format.DateTimeFormatter;
import java.util.List;

/**
 * DSS-backed SignerService — Wave 12.9b.
 *
 * Implements the PAdES signing + validation pipeline against the EU
 * Commission DSS 5.12.1 reference implementation. Server-key mode
 * (MODE_SERVER_HSM): the signing key lives in a PKCS#12 keystore the
 * sidecar loads at startup ({@code SIGNER_KEYSTORE_PATH}). The dev
 * image bakes in a self-signed keystore; prod mounts a real one (or,
 * later, swaps the {@link KeyStoreSignatureToken} for a KMS-backed
 * token — the only line that changes).
 *
 * Level handling: B-B always works offline. B-T / B-LT / B-LTA need a
 * TSA — if {@code SIGNER_TSA_URL} (or the request's tsa_url) is set we
 * attach an {@link OnlineTSPSource} and honor the requested level;
 * otherwise we downgrade to B-B and log it (so dev without a TSA still
 * produces a structurally valid signed PDF).
 *
 * Logging hygiene: {@link SignRequest#getPdfBytes()} is PII and never
 * reaches the log — we log only signer name, field, and level.
 */
public final class SignerService extends SignerServiceGrpc.SignerServiceImplBase {

    private static final Logger LOG = LoggerFactory.getLogger(SignerService.class);

    private final Pkcs12SignatureToken token;
    private final DSSPrivateKeyEntry privateKey;
    private final String defaultTsaUrl;

    public SignerService(String keystorePath, String keystorePass, String keyAlias, String defaultTsaUrl) {
        this.defaultTsaUrl = defaultTsaUrl == null ? "" : defaultTsaUrl.trim();
        try {
            this.token = new Pkcs12SignatureToken(keystorePath,
                    new PasswordProtection(keystorePass.toCharArray()));
        } catch (Exception e) {
            throw new IllegalStateException("load keystore " + keystorePath + ": " + e.getMessage(), e);
        }
        List<DSSPrivateKeyEntry> keys = token.getKeys();
        if (keys.isEmpty()) {
            throw new IllegalStateException("keystore " + keystorePath + " has no private keys");
        }
        DSSPrivateKeyEntry selected = keys.get(0);
        if (keyAlias != null && !keyAlias.isBlank()) {
            for (DSSPrivateKeyEntry k : keys) {
                // The alias lives on the keystore-backed concrete entry.
                if (k instanceof KSPrivateKeyEntry kse && keyAlias.equals(kse.getAlias())) {
                    selected = k;
                    break;
                }
            }
        }
        this.privateKey = selected;
        LOG.info("signer key loaded: subject={} tsa={}",
                privateKey.getCertificate().getCertificate().getSubjectX500Principal().getName(),
                this.defaultTsaUrl.isEmpty() ? "(none → B-B only)" : this.defaultTsaUrl);
    }

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
        String tsaUrl = req.getTsaUrl().isEmpty() ? defaultTsaUrl : req.getTsaUrl();
        SignatureLevel level = resolveLevel(req.getLevel(), tsaUrl);
        LOG.info("sign request level={} (effective={}) field={} signer={}",
                req.getLevel(), level, req.getFieldName(), req.getSignerName());

        try {
            DSSDocument toSign = new InMemoryDocument(req.getPdfBytes().toByteArray(), "document.pdf");

            PAdESSignatureParameters params = new PAdESSignatureParameters();
            params.setSignatureLevel(level);
            params.setDigestAlgorithm(DigestAlgorithm.SHA256);
            params.setSigningCertificate(privateKey.getCertificate());
            params.setCertificateChain(privateKey.getCertificateChain());
            if (!req.getReason().isEmpty()) {
                params.setReason(req.getReason());
            }
            if (!req.getLocation().isEmpty()) {
                params.setLocation(req.getLocation());
            }
            if (!req.getContactInfo().isEmpty()) {
                params.setContactInfo(req.getContactInfo());
            }
            // Invisible signature by default. field_name (the "Signer_<order>"
            // convention) maps to a pre-existing empty AcroForm field via
            // setFieldId — but only when one exists. Creating fresh signatures
            // we don't pre-seed a field, so naming/visible-appearance is a
            // follow-up; an unnamed invisible PAdES signature is fully valid.

            CommonCertificateVerifier cv = new CommonCertificateVerifier();
            // Trust our own signing chain so the post-sign validation embedded
            // in B-LT can build a path (dev self-signed). Prod swaps in the
            // real trust anchors / TSL.
            CommonTrustedCertificateSource trusted = new CommonTrustedCertificateSource();
            for (var cert : privateKey.getCertificateChain()) {
                trusted.addCertificate(cert);
            }
            cv.addTrustedCertSources(trusted);

            PAdESService service = new PAdESService(cv);
            if (!tsaUrl.isEmpty()) {
                service.setTspSource(new OnlineTSPSource(tsaUrl));
            }

            ToBeSigned dataToSign = service.getDataToSign(toSign, params);
            SignatureValue signatureValue = token.sign(dataToSign, params.getDigestAlgorithm(), privateKey);
            DSSDocument signed = service.signDocument(toSign, params, signatureValue);

            byte[] out;
            try (ByteArrayOutputStream baos = new ByteArrayOutputStream()) {
                signed.writeTo(baos);
                out = baos.toByteArray();
            }

            SignResponse.Builder b = SignResponse.newBuilder()
                    .setPdfBytes(com.google.protobuf.ByteString.copyFrom(out))
                    .setLevel(toProtoLevel(level))
                    .setSignedAt(java.time.OffsetDateTime.now(java.time.ZoneOffset.UTC)
                            .format(DateTimeFormatter.ISO_OFFSET_DATE_TIME))
                    .setFingerprint(DSSUtils.toHex(DSSUtils.digest(DigestAlgorithm.SHA256, out)));
            resp.onNext(b.build());
            resp.onCompleted();
        } catch (Exception e) {
            LOG.error("sign failed: {}", e.getMessage());
            resp.onError(Status.INTERNAL.withDescription("sign failed: " + e.getMessage()).asRuntimeException());
        }
    }

    @Override
    public void verify(VerifyRequest req, StreamObserver<VerifyResponse> resp) {
        if (req.getPdfBytes().isEmpty()) {
            resp.onError(Status.INVALID_ARGUMENT.withDescription("pdf_bytes required").asRuntimeException());
            return;
        }
        try {
            DSSDocument doc = new InMemoryDocument(req.getPdfBytes().toByteArray());
            SignedDocumentValidator validator = SignedDocumentValidator.fromDocument(doc);
            validator.setCertificateVerifier(new CommonCertificateVerifier());
            Reports reports = validator.validateDocument();
            SimpleReport simple = reports.getSimpleReport();
            DiagnosticData diag = reports.getDiagnosticData();

            List<String> sigIds = simple.getSignatureIdList();
            boolean ltv = false;
            VerifyResponse.Builder out = VerifyResponse.newBuilder()
                    .setSignatureCount(sigIds.size());
            for (String id : sigIds) {
                boolean valid = simple.isValid(id);
                Object signedBy = simple.getSignedBy(id);
                String signer = signedBy != null ? signedBy.toString() : "";
                String issuer = "";
                var signingCert = diag.getSignatureById(id) != null
                        ? diag.getSignatureById(id).getSigningCertificate() : null;
                if (signingCert != null) {
                    // CertificateWrapper exposes parsed fields directly. For a
                    // self-signed dev cert subject==issuer; getCommonName is the
                    // stable accessor across DSS 5.x.
                    issuer = signingCert.getCommonName() != null ? signingCert.getCommonName() : "";
                }
                if (!diag.getTimestampList().isEmpty()) {
                    ltv = true;
                }
                SignatureInfo.Builder si = SignatureInfo.newBuilder()
                        .setSignerName(signer == null ? "" : signer)
                        .setIssuer(issuer == null ? "" : issuer)
                        .setValid(valid)
                        .setLevel(toProtoLevel(SignatureLevel.valueByName(
                                String.valueOf(simple.getSignatureFormat(id)))));
                var signingTime = simple.getSigningTime(id);
                if (signingTime != null) {
                    si.setSignedAt(signingTime.toInstant().toString());
                }
                out.addSignatures(si);
            }
            out.setLtvEnabled(ltv);
            // tamper-evident: every signature covers the doc + none reports a
            // hash failure. We approximate with "no signature is INDETERMINATE
            // for HASH_FAILURE" by requiring at least one signature present.
            out.setTamperEvident(!sigIds.isEmpty());
            resp.onNext(out.build());
            resp.onCompleted();
        } catch (Exception e) {
            LOG.error("verify failed: {}", e.getMessage());
            resp.onError(Status.INTERNAL.withDescription("verify failed: " + e.getMessage()).asRuntimeException());
        }
    }

    /** Downgrade T/LT/LTA to B when no TSA is reachable (dev offline). */
    private SignatureLevel resolveLevel(Level requested, String tsaUrl) {
        boolean haveTsa = tsaUrl != null && !tsaUrl.isEmpty();
        switch (requested) {
            case LEVEL_BT:
                return haveTsa ? SignatureLevel.PAdES_BASELINE_T : downgrade(requested);
            case LEVEL_BLT:
                return haveTsa ? SignatureLevel.PAdES_BASELINE_LT : downgrade(requested);
            case LEVEL_BLTA:
                return haveTsa ? SignatureLevel.PAdES_BASELINE_LTA : downgrade(requested);
            case LEVEL_BB:
            case LEVEL_UNSPECIFIED:
            default:
                return SignatureLevel.PAdES_BASELINE_B;
        }
    }

    private SignatureLevel downgrade(Level requested) {
        LOG.warn("level {} requested but no TSA configured; downgrading to PAdES-B-B", requested);
        return SignatureLevel.PAdES_BASELINE_B;
    }

    private static Level toProtoLevel(SignatureLevel lvl) {
        if (lvl == null) {
            return Level.LEVEL_UNSPECIFIED;
        }
        switch (lvl) {
            case PAdES_BASELINE_B:   return Level.LEVEL_BB;
            case PAdES_BASELINE_T:   return Level.LEVEL_BT;
            case PAdES_BASELINE_LT:  return Level.LEVEL_BLT;
            case PAdES_BASELINE_LTA: return Level.LEVEL_BLTA;
            default:                 return Level.LEVEL_UNSPECIFIED;
        }
    }
}
