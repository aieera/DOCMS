package io.sedoc.signer;

import static org.junit.jupiter.api.Assertions.assertEquals;

import io.grpc.MethodDescriptor;
import io.sedoc.signer.grpc.SignerServiceGrpc;
import java.util.List;
import java.util.stream.Collectors;
import org.junit.jupiter.api.Test;

class SignerContractTest {

    /**
     * The sidecar serves Sign only. Its Verify RPC reported tamper_evident
     * as "at least one signature present" with no trust anchors (S-8);
     * verification lives in the Go pades verifier.
     */
    @Test
    void serviceExposesOnlySign() {
        List<String> methods = SignerServiceGrpc.getServiceDescriptor().getMethods().stream()
                .map(MethodDescriptor::getBareMethodName)
                .sorted()
                .collect(Collectors.toList());
        assertEquals(List.of("Sign"), methods);
    }
}
