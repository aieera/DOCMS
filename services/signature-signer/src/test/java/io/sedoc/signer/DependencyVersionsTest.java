package io.sedoc.signer;

import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.io.IOException;
import java.io.InputStream;
import java.util.Properties;
import org.bouncycastle.jce.provider.BouncyCastleProvider;
import org.junit.jupiter.api.Test;

/**
 * Floors for the dependencies with known CVEs (S-6): DSS 5.12.1 pulled
 * BouncyCastle < 1.78; grpc 1.63, protobuf-java < 3.25.5 and logback 1.5.6
 * are affected too. A downgrade fails the image build.
 */
class DependencyVersionsTest {

    @Test
    void dssIsAtLeast6() throws IOException {
        assertAtLeast("dss-pades", pomVersion("eu.europa.ec.joinup.sd-dss", "dss-pades"), "6.0");
    }

    @Test
    void bouncyCastleIsAtLeast178() {
        String v = new BouncyCastleProvider().getVersionStr();
        assertAtLeast("bcprov", v, "1.78");
    }

    @Test
    void protobufIsAtLeast3255() throws Exception {
        // protobuf-java is built with Bazel: no pom.properties, but an OSGi manifest.
        assertAtLeast("protobuf-java", bundleVersion(com.google.protobuf.Message.class), "3.25.5");
    }

    @Test
    void grpcIsAtLeast166() {
        String v = io.grpc.internal.GrpcUtil.IMPLEMENTATION_VERSION;
        assertAtLeast("grpc", v, "1.66");
    }

    @Test
    void logbackIsAtLeast1513() throws IOException {
        assertAtLeast("logback-classic", pomVersion("ch.qos.logback", "logback-classic"), "1.5.13");
    }

    private static String pomVersion(String group, String artifact) throws IOException {
        String path = "META-INF/maven/" + group + "/" + artifact + "/pom.properties";
        try (InputStream in = DependencyVersionsTest.class.getClassLoader().getResourceAsStream(path)) {
            assertNotNull(in, "no " + path + " on the classpath");
            Properties p = new Properties();
            p.load(in);
            return p.getProperty("version");
        }
    }

    private static String bundleVersion(Class<?> inJar) throws Exception {
        java.net.URL loc = inJar.getProtectionDomain().getCodeSource().getLocation();
        try (java.util.jar.JarFile jar = new java.util.jar.JarFile(new java.io.File(loc.toURI()))) {
            java.util.jar.Attributes a = jar.getManifest().getMainAttributes();
            String v = a.getValue("Bundle-Version");
            return v != null ? v : a.getValue("Implementation-Version");
        }
    }

    private static void assertAtLeast(String what, String actual, String floor) {
        assertNotNull(actual, what + " version unknown");
        assertTrue(compare(actual, floor) >= 0, what + " " + actual + " is below " + floor);
    }

    /** Compares the leading numeric dotted parts; qualifiers are ignored. */
    static int compare(String a, String b) {
        String[] x = a.split("[.\\-]");
        String[] y = b.split("[.\\-]");
        for (int i = 0; i < Math.max(x.length, y.length); i++) {
            int xi = i < x.length ? leadingInt(x[i]) : 0;
            int yi = i < y.length ? leadingInt(y[i]) : 0;
            if (xi != yi) {
                return Integer.compare(xi, yi);
            }
        }
        return 0;
    }

    private static int leadingInt(String s) {
        int n = 0;
        for (char c : s.toCharArray()) {
            if (c < '0' || c > '9') {
                break;
            }
            n = n * 10 + (c - '0');
        }
        return n;
    }
}
