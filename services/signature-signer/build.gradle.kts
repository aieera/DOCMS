// SeDoc signature-signer — Java sidecar, Wave 12.9.
//
// Pinned versions:
//   - DSS 5.12.1 (LGPL-2.1; EU Commission eIDAS reference impl)
//   - grpc-java 1.63.0 (netty transport; Apache-2.0)
//   - protobuf-java 3.25.3
//   - Java 17 LTS (matches distroless runtime in Dockerfile)
//
// Output:
//   - build/libs/signer-all.jar (fat jar with dependencies shaded)
//
// Bundle size check: ./gradlew build && du -h build/libs/signer-all.jar
// expect ~35 MB (DSS pulls in BouncyCastle + XMLSec).

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

repositories {
    mavenCentral()
    // DSS releases land on EU Commission's nexus; mavenCentral mirrors them.
    maven("https://ec.europa.eu/digital-building-blocks/artifact/repository/esignaturedss/")
}

dependencies {
    // ---- DSS ----------------------------------------------------------------
    val dssVersion = "5.12.1"
    implementation("eu.europa.ec.joinup.sd-dss:dss-pades:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-pades-pdfbox:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-utils-apache-commons:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-service:$dssVersion")
    // Timestamp + OCSP
    implementation("eu.europa.ec.joinup.sd-dss:dss-token:$dssVersion")
    implementation("eu.europa.ec.joinup.sd-dss:dss-tsl-validation:$dssVersion")

    // ---- gRPC ---------------------------------------------------------------
    val grpcVersion = "1.63.0"
    implementation("io.grpc:grpc-netty-shaded:$grpcVersion")
    implementation("io.grpc:grpc-protobuf:$grpcVersion")
    implementation("io.grpc:grpc-stub:$grpcVersion")
    // javax.annotation.Generated is used by the generated stubs.
    compileOnly("org.apache.tomcat:annotations-api:6.0.53")

    // ---- Logging ------------------------------------------------------------
    implementation("ch.qos.logback:logback-classic:1.5.6")

    // ---- Testing ------------------------------------------------------------
    testImplementation("org.junit.jupiter:junit-jupiter:5.10.2")
    testImplementation("io.grpc:grpc-testing:$grpcVersion")
}

application {
    mainClass.set("io.sedoc.signer.SignerServer")
}

// Proto generation — signer.proto compiles to Java stubs that
// SignerService extends.
protobuf {
    protoc {
        artifact = "com.google.protobuf:protoc:3.25.3"
    }
    plugins {
        create("grpc") {
            artifact = "io.grpc:protoc-gen-grpc-java:1.63.0"
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
