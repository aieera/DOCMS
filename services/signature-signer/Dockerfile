# Wave 12.9b — distroless JRE 17 image for the DSS signer sidecar.
#
# Three-stage build:
#   1. gradle:8.7-jdk17 compiles, runs the JUnit tests, and shadow-jars
#      DSS + gRPC (gradle is preinstalled, so no committed wrapper is
#      needed). A failing test fails the image build.
#   2. eclipse-temurin keytool bakes a self-signed DEV signing keystore.
#      The signer refuses it at startup (exit 2) unless
#      SIGNER_ALLOW_DEV_KEY=1 — set that on a local stack only. Prod
#      mounts a real PKCS#12 and sets SIGNER_KEYSTORE_PATH /
#      SIGNER_KEYSTORE_PASS; the dev key is also recognised by its
#      subject CN if it is copied elsewhere.
#   3. distroless/java17 runs the fat jar.
#
# Image size target: < 250 MB (DSS + grpc-netty-shaded + logback ~140 MB
# on top of the 30 MB distroless base).

FROM gradle:8.7-jdk17 AS build
WORKDIR /src
COPY settings.gradle.kts build.gradle.kts ./
COPY src ./src
RUN --mount=type=cache,target=/home/gradle/.gradle \
    gradle --no-daemon test shadowJar

# Dev signing material — self-signed, 10y, RSA-2048. PAdES-B-B valid;
# higher levels need a real CA + TSA (SIGNER_TSA_URL) in prod. The CN
# "SeDoc Dev Signer" is what DevKeyPolicy matches — keep them in sync.
FROM eclipse-temurin:17-jdk AS keygen
RUN keytool -genkeypair -alias signer -keyalg RSA -keysize 2048 -validity 3650 \
    -dname "CN=SeDoc Dev Signer,O=SeDoc,OU=Signing,C=US" \
    -ext "KeyUsage=digitalSignature,nonRepudiation" -ext "ExtendedKeyUsage=1.3.6.1.5.5.7.3.4" \
    -storetype PKCS12 -keystore /dev-signer.p12 \
    -storepass changeit -keypass changeit

# Runtime: eclipse-temurin JRE. (distroless/java17 is leaner + the prod
# preference, but its locally-cached layer was truncated by a disk-full event
# — libz.so.1 "file too short"; temurin's layers are intact. Re-pin distroless
# on a healthy host for prod.)
FROM eclipse-temurin:17-jre
COPY --from=build /src/build/libs/signer-all.jar /app/signer-all.jar
COPY --from=keygen /dev-signer.p12 /app/dev-signer.p12
# The path defaults to the dev keystore for local convenience only. No
# password is baked: the signer supplies the dev keystore's password only
# with SIGNER_ALLOW_DEV_KEY=1, and otherwise exits instead of signing with
# it. Production sets SIGNER_KEYSTORE_PATH + SIGNER_KEYSTORE_PASS.
ENV SIGNER_KEYSTORE_PATH=/app/dev-signer.p12 \
    SIGNER_KEY_ALIAS=signer
EXPOSE 6060
ENTRYPOINT ["java", "-jar", "/app/signer-all.jar"]
