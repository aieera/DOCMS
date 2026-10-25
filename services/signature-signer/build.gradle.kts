// SeDoc signature-signer — Java sidecar, Wave 12.9.
//
// Pinned versions (S-6 — floors enforced by DependencyVersionsTest):
//   - DSS 6.3 (LGPL-2.1; EU Commission eIDAS reference impl;
//     pulls BouncyCastle >= 1.78 and PDFBox 3)
//   - grpc-java 1.76.3 (netty transport; Apache-2.0)
//   - protobuf-java 3.25.8 (>= 3.25.5)
//   - logback 1.5.18 (>= 1.5.13)
//   - httpcore5 / httpcore5-h2 5.4.4 (>= 5.4.3; constraint over DSS's 5.3.4)
//   - Java 17 LTS (matches the JRE runtime in Dockerfile)
//
// Output:
//   - build/libs/signer-all.jar (fat jar with dependencies shaded)
//
// Bundle size check: gradle build && du -h build/libs/signer-all.jar
// (DSS pulls in BouncyCastle + PDFBox).

plugins {
    java
    application
    id("com.github.johnrengelman.shadow") version "8.1.1"
    id("com.google.protobuf") version "0.9.4"
}

group = "io.sedoc"
version = "0.1.0"

java {
    toolchain {
        languageVersion.set(JavaLanguageVersion.of(17))
    }
}

val protobufVersion = "3.25.8"
val grpcVersion = "1.76.3"

repositories {
    mavenCentral()
    // DSS releases land on EU Commission's nexus; mavenCentral mirrors them.
    maven("https://ec.europa.eu/digital-building-blocks/artifact/repository/esignaturedss/")
}

dependencies {
    // ---- DSS ----------------------------------------------------------------
    val dssVersion = "6.3"
    implementation("eu.europa.ec.joinup.sd-dss:dss-pades:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-pades-pdfbox:$dssVersion")
    // DSS 6 loads these through ServiceLoader and fails at runtime without
    // one: the CMS builder (object model) and the CRL parser used when
    // collecting revocation data for B-LT.
    implementation("eu.europa.ec.joinup.sd-dss:dss-cms-object:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-crl-parser-stream:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-utils-apache-commons:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-service:$dssVersion")
    // Timestamp + OCSP
    implementation("eu.europa.ec.joinup.sd-dss:dss-token:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-tsl-validation:$dssVersion")
    // dss-service 6.3 pulls httpclient5 5.5 -> httpcore5 / httpcore5-h2 5.3.4:
    // CVE-2026-54399 (excessive headers) and CVE-2026-54428 (HPACK) are fixed
    // in 5.4.3. Raise both to the current 5.4.x patch; the floor is enforced
    // by DependencyVersionsTest.
    constraints {
        implementation("org.apache.httpcomponents.core5:httpcore5:5.4.4") {
            because("CVE-2026-54399, fixed in 5.4.3")
        }
        implementation("org.apache.httpcomponents.core5:httpcore5-h2:5.4.4") {
            because("CVE-2026-54428, fixed in 5.4.3")
        }
    }

    // ---- gRPC / protobuf ----------------------------------------------------
    implementation("io.grpc:grpc-netty-shaded:$grpcVersion")
    implementation("io.grpc:grpc-protobuf:$grpcVersion")
    implementation("io.grpc:grpc-stub:$grpcVersion")
    implementation("com.google.protobuf:protobuf-java:$protobufVersion")
    // javax.annotation.Generated is used by the generated stubs.
    compileOnly("org.apache.tomcat:annotations-api:6.0.53")

    // ---- Logging ------------------------------------------------------------
    implementation("ch.qos.logback:logback-classic:1.5.18")

    // ---- Testing ------------------------------------------------------------
    testImplementation(platform("org.junit:junit-bom:5.10.2"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    testImplementation("io.grpc:grpc-testing:$grpcVersion")
}

application {
    mainClass.set("io.sedoc.signer.SignerServer")
}

// Proto generation — signer.proto compiles to Java stubs that
// SignerService extends.
protobuf {
    protoc {
        artifact = "com.google.protobuf:protoc:$protobufVersion"
    }
    plugins {
        create("grpc") {
            artifact = "io.grpc:protoc-gen-grpc-java:$grpcVersion"
        }
    }
    generateProtoTasks {
        all().forEach {
            it.plugins {
                create("grpc") {}
            }
        }
    }
}

tasks.withType<JavaCompile>().configureEach {
    options.encoding = "UTF-8"
    // -Xlint:all (no -Werror): the generated protobuf/gRPC stubs and DSS's
    // own deprecations emit warnings we don't control; failing the build on
    // them is unworkable. Keep lint visible, don't gate on it.
    options.compilerArgs.add("-Xlint:all,-processing,-deprecation")
}

tasks.test {
    useJUnitPlatform()
    // The image build runs the tests; print failures in full so the
    // docker build log says why.
    testLogging {
        events("failed")
        exceptionFormat = org.gradle.api.tasks.testing.logging.TestExceptionFormat.FULL
    }
}

// shadowJar produces the single-file runtime artifact the
// Dockerfile copies into the distroless base.
tasks.named<com.github.jengelman.gradle.plugins.shadow.tasks.ShadowJar>("shadowJar") {
    // Fixed name the Dockerfile copies (build/libs/signer-all.jar) — without
    // this the artifact would be signature-signer-0.1.0-all.jar and the COPY
    // in the image build would fail.
    archiveFileName.set("signer-all.jar")
    mergeServiceFiles()
    manifest {
        attributes["Main-Class"] = "io.sedoc.signer.SignerServer"
    }
}

tasks.build { dependsOn(tasks.shadowJar) }
