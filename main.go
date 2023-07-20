package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/go-logr/zapr"
	kubenotation "github.com/nirmata/kubenotation"
	knvSetup "github.com/nirmata/kyverno-notation-verifier/setup"
	knvVerifier "github.com/nirmata/kyverno-notation-verifier/verifier"
	_ "github.com/notaryproject/notation-core-go/signature/cose"
	_ "github.com/notaryproject/notation-core-go/signature/jws"
	"go.uber.org/zap"
)

func main() {
	var flagLocal bool
	flag.BoolVar(&flagLocal, "local", false, "Use local system notation configuration")

	var flagNoTLS bool
	flag.BoolVar(&flagNoTLS, "notls", false, "Do not start the TLS server")

	var flagImagePullSecrets string
	flag.StringVar(&flagImagePullSecrets, "imagePullSecrets", "", "Secret resource names for image registry access credentials.")

	var flagAllowInsecureRegistry bool
	flag.BoolVar(&flagAllowInsecureRegistry, "allowInsecureRegistry", false, "Whether to allow insecure connections to registries. Not recommended.")

	var flagNotationPluginConfigMap string
	flag.StringVar(&flagNotationPluginConfigMap, "pluginConfigMap", "notation-plugin-config", "ConfigMap with notation plugin configuration")

	var flagEnableDebug bool
	flag.BoolVar(&flagEnableDebug, "debug", false, "Enable debug logging")

	var flagMaxSignatureAtempts int
	flag.IntVar(&flagMaxSignatureAtempts, "maxSignatureAttempts", 30, "Maximum number of signature envelopes that will be processed for verification")

	var metricsAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")

	var probeAddr string
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")

	var enableLeaderElection bool
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	flag.Parse()
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}

	slog := logger.Sugar().WithOptions(zap.AddStacktrace(zap.DPanicLevel))

	errKN := make(chan error, 1)
	go func() {
		errKN <- kubenotation.Start(zapr.NewLogger(logger), metricsAddr, probeAddr, enableLeaderElection)
	}()

	if !flagLocal {
		knvSetup.SetupLocal(slog)
	}

	verifier := knvVerifier.NewVerifier(slog,
		knvVerifier.WithImagePullSecrets(flagImagePullSecrets),
		knvVerifier.WithInsecureRegistry(flagAllowInsecureRegistry),
		knvVerifier.WithPluginConfig(flagNotationPluginConfigMap),
		knvVerifier.WithMaxSignatureAttempts(flagMaxSignatureAtempts),
		knvVerifier.WithEnableDebug(flagEnableDebug),
		knvVerifier.WithProviderAuthConfigResolver(getAuthFromIRSA))

	mux := http.NewServeMux()
	mux.HandleFunc("/checkimages", verifier.HandleCheckImages)
	errsHTTP := make(chan error, 1)
	go func() {
		errsHTTP <- http.ListenAndServe(":9080", mux)
	}()

	errsTLS := make(chan error, 1)
	if !flagNoTLS {
		go func() {
			errsTLS <- http.ListenAndServeTLS(":9443", knvVerifier.CertFile, knvVerifier.KeyFile, mux)
		}()
	}

	slog.Info("Listening...")
	select {
	case err := <-errsHTTP:
		slog.Infof("HTTP server error: %v", err)
	case err := <-errsTLS:
		slog.Infof("TLS server error: %v", err)
	case err := <-errKN:
		slog.Infof("failed to initialize crds: %v", err)
	}

	verifier.Stop()
	os.Exit(-1)
}
