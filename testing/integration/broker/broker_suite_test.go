package broker_test

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"testing"
	"text/template"

	"github.com/alphagov/paas-s3-broker/s3"
	"github.com/alphagov/paas-service-broker-base/broker"
	"github.com/alphagov/paas-service-broker-base/testing/mock_locket_server"
	"github.com/aws/aws-sdk-go/service/iam/iamiface"
	uuid "github.com/satori/go.uuid"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/iam"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var (
	BrokerSuiteData SuiteData
	mockLocket      *mock_locket_server.MockLocket
	locketFixtures  mock_locket_server.LocketFixtures
)

type SuiteData struct {
	LocalhostIAMPolicyArn *string
	EgressIPIAMPolicyARN  *string
	AWSRegion             string
}

func TestBroker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Broker Suite")
}

var _ = BeforeSuite(func() {
	file, err := os.Open("../../fixtures/config.json")
	Expect(err).ToNot(HaveOccurred())
	defer file.Close()

	config, err := broker.NewConfig(file)
	Expect(err).ToNot(HaveOccurred())
	s3ClientConfig, err := s3.NewS3ClientConfig(config.Provider)
	Expect(err).ToNot(HaveOccurred())

	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(s3ClientConfig.AWSRegion)}))
	iamClient := iam.New(sess)
	createLocalhostIAMPolicyOutput := createLocalhostPolicy(iamClient)
	createEgressIPIAMPolicyOutput := createEgressIPPolicy(iamClient)

	// Start test Locket server
	locketFixtures, err = mock_locket_server.SetupLocketFixtures()
	Expect(err).NotTo(HaveOccurred())
	mockLocket, err = mock_locket_server.New("keyBasedLock", locketFixtures.Filepath)
	Expect(err).NotTo(HaveOccurred())
	mockLocket.Start(mockLocket.Logger, mockLocket.ListenAddress, mockLocket.Certificate, mockLocket.Handler)

	BrokerSuiteData = SuiteData{
		LocalhostIAMPolicyArn: createLocalhostIAMPolicyOutput.Policy.Arn,
		EgressIPIAMPolicyARN:  createEgressIPIAMPolicyOutput.Policy.Arn,
		AWSRegion:             s3ClientConfig.AWSRegion,
	}
})

var _ = AfterSuite(func() {
	if mockLocket != nil {
		mockLocket.Stop()
	}
	locketFixtures.Cleanup()

	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(BrokerSuiteData.AWSRegion)}))
	iamClient := iam.New(sess)

	for _, arn := range []*string{BrokerSuiteData.LocalhostIAMPolicyArn, BrokerSuiteData.EgressIPIAMPolicyARN} {
		if arn != nil {
			_, err := iamClient.DeletePolicy(&iam.DeletePolicyInput{
				PolicyArn: arn,
			})

			Expect(err).NotTo(HaveOccurred())
		}
	}
})

func createLocalhostPolicy(iamClient iamiface.IAMAPI) *iam.CreatePolicyOutput {
	policyString, err := generatePolicy("127.0.0.1/32")

	uniqPolicyName := fmt.Sprintf("TestS3BrokerIpRestrictionLocalhost-%s", uuid.NewV4())
	createDefaultIAMPolicyOutput, err := iamClient.CreatePolicy(&iam.CreatePolicyInput{
		Description:    aws.String("Integration Test S3 Broker IP restriction policy - restricted to localhost only"),
		PolicyDocument: policyString,
		PolicyName:     aws.String(uniqPolicyName),
	})
	Expect(err).NotTo(HaveOccurred())

	return createDefaultIAMPolicyOutput
}
func createEgressIPPolicy(iamClient *iam.IAM) *iam.CreatePolicyOutput {
	resp, err := http.Get("https://wtfismyip.com/text")
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK))

	body, err := ioutil.ReadAll(resp.Body)
	Expect(err).ToNot(HaveOccurred())

	ip := strings.TrimSpace(string(body))
	policyString, err := generatePolicy(fmt.Sprintf("%s/32", ip))
	Expect(err).ToNot(HaveOccurred())

	uniqPolicyName := fmt.Sprintf("TestS3BrokerIpRestriction%s-%s", ip, uuid.NewV4())
	createEgressIPIAMPolicyOutput, err := iamClient.CreatePolicy(&iam.CreatePolicyInput{
		Description:    aws.String("Integration Test S3 Broker IP restriction policy - restricted to egress ip only"),
		PolicyDocument: policyString,
		PolicyName:     aws.String(uniqPolicyName),
	})

	Expect(err).ToNot(HaveOccurred())
	return createEgressIPIAMPolicyOutput
}

func generatePolicy(ip string) (*string, error) {
	t, err := template.ParseFiles("../../fixtures/test_s3_broker_ip_restriction_iam_policy.json.tpl")
	if err != nil {
		return nil, err
	}

	buffer := bytes.Buffer{}
	bufferWriter := io.Writer(&buffer)
	err = t.Execute(bufferWriter, map[string]string{"ip": ip})

	if err != nil {
		return nil, err
	}

	out := buffer.String()
	return &out, nil
}
