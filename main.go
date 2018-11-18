package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sony/gobreaker"
)

// Initialize the Circuit Breaker
var conditionACircuitBreaker *gobreaker.TwoStepCircuitBreaker
var conditionBCircuitBreaker *gobreaker.TwoStepCircuitBreaker
var conditionaACallback func(bool)
var conditionaBCallback func(bool)

func init() {
	var st gobreaker.Settings
	st.Name = "CB"
	st.Timeout = 20 * time.Second
	st.OnStateChange = func(name string, from gobreaker.State, to gobreaker.State) {
		log.Printf("%s change state from %s to %s", name, from, to)
	}
	// Here we can define condition for tripping state of circuit breaker
	st.ReadyToTrip = func(counts gobreaker.Counts) bool {
		return counts.ConsecutiveFailures >= 3
	}
	conditionACircuitBreaker = gobreaker.NewTwoStepCircuitBreaker(st)
	conditionBCircuitBreaker = gobreaker.NewTwoStepCircuitBreaker(st)
}

type requestPayloadStruct struct {
	ProxyCondition string `json:"proxy_condition"`
}

func handleIfError(message string, err error) {
	if err != nil {
		log.Fatalf("%s: %s", message, err)
	}
}

// Get env var or default
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// Get the port to listen on
func getListenAddress() string {
	port := getEnv("PORT", "1338")
	return ":" + port
}

// Log the env variables required for a reverse proxy
func logSetup() {
	a_condtion_url := os.Getenv("A_CONDITION_URL")
	b_condtion_url := os.Getenv("B_CONDITION_URL")
	default_condtion_url := os.Getenv("DEFAULT_CONDITION_URL")

	log.Printf("Server will run on: %s\n", getListenAddress())
	log.Printf("Redirecting to A url: %s\n", a_condtion_url)
	log.Printf("Redirecting to B url: %s\n", b_condtion_url)
	log.Printf("Redirecting to Default url: %s\n", default_condtion_url)
}

// Get a json decoder for a given requests body
func requestBodyDecoder(request *http.Request) *json.Decoder {
	// Read body to buffer
	body, err := ioutil.ReadAll(request.Body)
	handleIfError("Error reading body", err)
	request.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	return json.NewDecoder(ioutil.NopCloser(bytes.NewBuffer(body)))
}

// Parse the requests body
func parseRequestBody(request *http.Request) requestPayloadStruct {
	decoder := requestBodyDecoder(request)
	var requestPayload requestPayloadStruct
	err := decoder.Decode(&requestPayload)
	handleIfError("Error decoding body", err)
	return requestPayload
}

// Log the typeform payload and redirect url
func logRequestPayload(requestionPayload requestPayloadStruct, proxyURL string) {
	log.Printf("proxy_condition: %s, proxy_url: %s\n", requestionPayload.ProxyCondition, proxyURL)
}

// Get the url for a given proxy condition
func getProxyUrl(proxyConditionRaw string) string {
	proxyCondition := strings.ToUpper(proxyConditionRaw)

	a_condtion_url := os.Getenv("A_CONDITION_URL")
	b_condtion_url := os.Getenv("B_CONDITION_URL")
	default_condtion_url := os.Getenv("DEFAULT_CONDITION_URL")

	if proxyCondition == "A" {
		return a_condtion_url
	}

	if proxyCondition == "B" {
		return b_condtion_url
	}

	return default_condtion_url
}

func getCircuitBreaker(url string) *gobreaker.TwoStepCircuitBreaker {
	if url == os.Getenv("A_CONDITION_URL") {
		return conditionACircuitBreaker
	}
	if url == os.Getenv("B_CONDITION_URL") {
		return conditionBCircuitBreaker
	}
	return nil
}

type myProxyTransport struct {
	successReporter func(bool)
}

// RoundTrip - inrecepts default transport implementation to be abple to
// report success status , this will guarantee that HalfOpen Circuit breaker will close
// after first successful attempt
func (mpt *myProxyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(request)
	if err == nil {
		// notify circuit breaker about success
		log.Printf("No errrors - reporting success")
		mpt.successReporter(true)
	}
	return response, err
}

// Serve a reverse proxy for a given url
func serveReverseProxy(target string, res http.ResponseWriter, req *http.Request) {
	// parse the url
	backendURL, err := url.Parse(target)
	handleIfError("Error, couldn't parse url from "+target, err)

	// Update the headers to allow for SSL redirection
	req.URL.Host = backendURL.Host
	req.URL.Scheme = backendURL.Scheme
	req.Header.Set("X-Forwarded-Host", req.Header.Get("Host"))
	req.Host = backendURL.Host
	cb := getCircuitBreaker(target)

	log.Printf("Start of serveReverseProxy: current cb state %s", cb.State())
	suceessCallback, err := cb.Allow()

	if err == nil || cb.State() == gobreaker.StateHalfOpen {
		// create the reverse proxy
		proxy := httputil.NewSingleHostReverseProxy(backendURL)
		// Configure circuit breaker to close if error occures during serving the proxy request
		proxy.ErrorHandler = func(resp http.ResponseWriter, req *http.Request, err error) {
			log.Printf("Error %s , reporting CB failure, current state %s", err, cb.State())
			suceessCallback(false)
			res.WriteHeader(http.StatusRequestTimeout)
			res.Write([]byte(fmt.Sprintf("Circuit breaker %s, Err %s", cb.State(), err)))
		}
		proxy.Transport = &myProxyTransport{suceessCallback}

		// Proxy serve the request
		proxy.ServeHTTP(res, req)
		// Check this https://stackoverflow.com/questions/21270945/how-to-read-the-response-from-a-newsinglehostreverseproxy
		// to implement your own roundTripper which will notify about success
		// only then you should be able to let circuit breaker know that HalOpen breaker
		// can be closed as request was successful , otherwise there is no way to close CB
	} else {
		res.WriteHeader(http.StatusRequestTimeout)
		res.Write([]byte(fmt.Sprintf("Circuit breaker %s, Err %s", cb.State(), err)))
	}

}

// Given a request send it to the appropriate url
func handleRequestAndRedirect(res http.ResponseWriter, req *http.Request) {
	requestPayload := parseRequestBody(req)
	proxyURL := getProxyUrl(requestPayload.ProxyCondition)
	// logRequestPayload(requestPayload, proxyURL)
	serveReverseProxy(proxyURL, res, req)
}

func main() {
	// Log setup values
	logSetup()
	// start server
	http.HandleFunc("/", handleRequestAndRedirect)
	err := http.ListenAndServe(getListenAddress(), nil)
	handleIfError("Error starting server", err)
}
