package io.sedoc.signer;

import io.grpc.Server;
import io.grpc.ServerBuilder;
import java.util.Map;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * gRPC server bootstrap. Binds on port 6060 (configurable via
 * SIGNER_PORT) and serves the one RPC interface the Go
 * signature service calls.
 *
 * Wave 12.9 ships the process lifecycle; Wave 12.9b fills in the
 * DSS PAdES implementation in {@link SignerService} (Sign). The
 * server is production-ready: signal handling, graceful shutdown,
 * startup logging. It refuses to start without an explicit keystore
 * and password, and with the baked development key unless
 * SIGNER_ALLOW_DEV_KEY=1 ({@link DevKeyPolicy}).
 */
public final class SignerServer {

    private static final Logger LOG = LoggerFactory.getLogger(SignerServer.class);

    /** Process configuration read from the environment. */
    record Config(int port, String keystorePath, String keystorePass, String keyAlias, String tsaUrl,
                  boolean allowDevKey) {

        /**
         * @throws IllegalArgumentException when SIGNER_KEYSTORE_PATH or
         *                                  SIGNER_KEYSTORE_PASS is missing (there is no
         *                                  default password any more — R-17)
         */
        static Config fromEnv(Map<String, String> env) {
            String ksPath = env.getOrDefault("SIGNER_KEYSTORE_PATH", "");
            if (ksPath.isBlank()) {
                throw new IllegalArgumentException("SIGNER_KEYSTORE_PATH is required (PKCS#12 signing keystore)");
            }
            String ksPass = env.getOrDefault("SIGNER_KEYSTORE_PASS", "");
            if (ksPass.isEmpty()) {
                throw new IllegalArgumentException("SIGNER_KEYSTORE_PASS is required");
            }
            return new Config(
                    Integer.parseInt(env.getOrDefault("SIGNER_PORT", "6060")),
                    ksPath, ksPass,
                    env.getOrDefault("SIGNER_KEY_ALIAS", ""),
                    env.getOrDefault("SIGNER_TSA_URL", ""),
                    "1".equals(env.get(DevKeyPolicy.OPT_IN_ENV)));
        }
    }

    public static void main(String[] args) throws Exception {
        Config cfg;
        SignerService service;
        try {
            cfg = Config.fromEnv(System.getenv());
            service = new SignerService(cfg.keystorePath(), cfg.keystorePass(), cfg.keyAlias(), cfg.tsaUrl(),
                    cfg.allowDevKey());
        } catch (IllegalArgumentException | IllegalStateException e) {
            LOG.error("signature-signer cannot start: {}", e.getMessage());
            System.exit(2);
            return;
        }
        int port = cfg.port();

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
