package io.sedoc.signer;

import java.io.OutputStream;
import java.math.BigInteger;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.KeyStore;
import java.security.cert.Certificate;
import java.security.cert.X509Certificate;
import java.util.Date;
import org.bouncycastle.asn1.x500.X500Name;
import org.bouncycastle.asn1.x509.Extension;
import org.bouncycastle.asn1.x509.KeyUsage;
import org.bouncycastle.cert.jcajce.JcaX509CertificateConverter;
import org.bouncycastle.cert.jcajce.JcaX509v3CertificateBuilder;
import org.bouncycastle.operator.jcajce.JcaContentSignerBuilder;

/** Test helpers: throwaway PKCS#12 signing keystores and a minimal PDF. */
final class TestKeystores {

    static final String PASSWORD = "test-pass";
    static final String ALIAS = "signer";

    /** Writes a PKCS#12 keystore holding a fresh self-signed RSA key for subject {@code dn}. */
    static Path selfSigned(Path dir, String dn) throws Exception {
        KeyPairGenerator kpg = KeyPairGenerator.getInstance("RSA");
        kpg.initialize(2048);
        KeyPair kp = kpg.generateKeyPair();
        X500Name name = new X500Name(dn);
        long now = System.currentTimeMillis();
        JcaX509v3CertificateBuilder builder = new JcaX509v3CertificateBuilder(
                name, BigInteger.valueOf(now), new Date(now - 3_600_000L), new Date(now + 86_400_000L),
                name, kp.getPublic());
        builder.addExtension(Extension.keyUsage, true,
                new KeyUsage(KeyUsage.digitalSignature | KeyUsage.nonRepudiation));
        X509Certificate cert = new JcaX509CertificateConverter().getCertificate(
                builder.build(new JcaContentSignerBuilder("SHA256withRSA").build(kp.getPrivate())));
        KeyStore ks = KeyStore.getInstance("PKCS12");
        ks.load(null, null);
        ks.setKeyEntry(ALIAS, kp.getPrivate(), PASSWORD.toCharArray(), new Certificate[] {cert});
        Path file = Files.createTempFile(dir, "signer", ".p12");
        try (OutputStream out = Files.newOutputStream(file)) {
            ks.store(out, PASSWORD.toCharArray());
        }
        return file;
    }

    /** A one-page PDF with a correct xref table. */
    static byte[] minimalPdf() {
        String[] objs = {
            "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
            "2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
            "3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>\nendobj\n",
        };
        StringBuilder sb = new StringBuilder("%PDF-1.4\n");
        int[] offsets = new int[objs.length];
        for (int i = 0; i < objs.length; i++) {
            offsets[i] = sb.length();
            sb.append(objs[i]);
        }
        int xref = sb.length();
        sb.append("xref\n0 ").append(objs.length + 1).append("\n0000000000 65535 f \n");
        for (int off : offsets) {
            sb.append(String.format("%010d 00000 n \n", off));
        }
        sb.append("trailer\n<< /Size ").append(objs.length + 1).append(" /Root 1 0 R >>\nstartxref\n")
                .append(xref).append("\n%%EOF\n");
        return sb.toString().getBytes(java.nio.charset.StandardCharsets.ISO_8859_1);
    }

    private TestKeystores() {}
}
