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
 * Wave 12.9 ships the process lifecycle. The actual DSS PAdES
 * implementation in {@link SignerService} is the shell that
 * Wave 12.9b fills in — the server is production-ready (signal
 * handling, graceful shutdown, startup logging); the service
 * method bodies return UNIMPLEMENTED until the DSS wiring lands.
 */
public final class SignerServer {

    private static final Logger LOG = LoggerFactory.getLogger(SignerServer.class);

    public static void main(String[] args) throws Exception {
        int port = Integer.parseInt(System.getenv().getOrDefault("SIGNER_PORT", "6060"));

        Server server = ServerBuilder.forPort(port)
                .addService(new SignerService())
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
