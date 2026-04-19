# Wave 12.9 — distroless JRE 17 image for the DSS signer sidecar.
#
# Two-stage build:
#   1. eclipse-temurin:17-jdk compiles + shadow-jars DSS + gRPC.
#   2. distroless/java17-debian12:nonroot runs it.
#
# Image size target: < 200 MB. DSS + grpc-netty-shaded + logback
# together produce ~140 MB of deps on top of a 30 MB distroless base.

FROM eclipse-temurin:17-jdk AS build
WORKDIR /src
COPY settings.gradle.kts build.gradle.kts ./
COPY src ./src
RUN --mount=type=cache,target=/root/.gradle \
    ./gradlew --no-daemon shadowJar

FROM gcr.io/distroless/java17-debian12:nonroot
COPY --from=build /src/build/libs/signer-all.jar /app/signer-all.jar
EXPOSE 6060
# distroless entrypoint already points at `java`; pass jar as args.
CMD ["/app/signer-all.jar"]
