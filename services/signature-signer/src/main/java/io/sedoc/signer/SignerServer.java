package io.sedoc.signer;

import io.grpc.Server;
import io.grpc.ServerBuilder;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * gRPC server bootstrap. Binds on port 6060 (configurable via
 * SIGNER_PORT) and serves the one RPC interface the Go
 * signature service calls.
 *
 * Wave 12.9 ships the process lifecycle; Wave 12.9b fills in the
 * DSS PAdES implementation in {@link SignerService} (Sign + Verify
 * are live). The server is production-ready: signal handling,
 * graceful shutdown, startup logging.
 */
public final class SignerServer {

    private static final Logger LOG = LoggerFactory.getLogger(SignerServer.class);

    public static void main(String[] args) throws Exception {
        int port = Integer.parseInt(System.getenv().getOrDefault("SIGNER_PORT", "6060"));

        String ksPath = System.getenv().getOrDefault("SIGNER_KEYSTORE_PATH", "");
        String ksPass = System.getenv().getOrDefault("SIGNER_KEYSTORE_PASS", "changeit");
        String alias = System.getenv().getOrDefault("SIGNER_KEY_ALIAS", "");
        String tsaUrl = System.getenv().getOrDefault("SIGNER_TSA_URL", "");
        if (ksPath.isBlank()) {
            LOG.error("SIGNER_KEYSTORE_PATH is required (PKCS#12 signing keystore)");
            System.exit(2);
        }

        SignerService service = new SignerService(ksPath, ksPass, alias, tsaUrl);

        Server server = ServerBuilder.forPort(port)
                .addService(service)
                .build()
                .start();

        LOG.info("signature-signer listening on :{}", port);

        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            LOG.info("SIGTERM received; draining");
            try {
                server.shutdown().awaitTermination();
            } catch (InterruptedException ie) {
                Thread.currentThread().interrupt();
                server.shutdownNow();
            }
        }, "signer-shutdown"));

        server.awaitTermination();
    }

    private SignerServer() {}
}
