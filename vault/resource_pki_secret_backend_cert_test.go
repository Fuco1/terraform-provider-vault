package vault

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/hashicorp/vault/api"

	"github.com/hashicorp/terraform-provider-vault/testutil"
)

type testPKICertStore struct {
	cert          string
	expectRevoked bool
}

func TestPkiSecretBackendCert_basic(t *testing.T) {
	rootPath := "pki-root-" + strconv.Itoa(acctest.RandInt())
	intermediatePath := "pki-intermediate-" + strconv.Itoa(acctest.RandInt())

	var store testPKICertStore

	resourceName := "vault_pki_secret_backend_cert.test"
	resource.Test(t, resource.TestCase{
		Providers:    testProviders,
		PreCheck:     func() { testutil.TestAccPreCheck(t) },
		CheckDestroy: testPkiSecretBackendCertDestroy,
		Steps: []resource.TestStep{
			{
				Config: testPkiSecretBackendCertConfig_basic(rootPath, intermediatePath, true, false),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "backend", intermediatePath),
					resource.TestCheckResourceAttr(resourceName, "common_name", "cert.test.my.domain"),
					resource.TestCheckResourceAttr(resourceName, "ttl", "720h"),
					resource.TestCheckResourceAttr(resourceName, "uri_sans.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "uri_sans.0", "spiffe://test.my.domain"),
					resource.TestCheckResourceAttr(resourceName, "revoke", "false"),
					testCapturePKICert(resourceName, &store),
				),
			},
			{
				// remove the cert to test revocation flow (expect no revocation)
				Config: testPkiSecretBackendCertConfig_basic(rootPath, intermediatePath, false, false),
				Check: resource.ComposeTestCheckFunc(
					testPKICertRevocation(intermediatePath, &store),
				),
			},
		},
	})
}

func TestPkiSecretBackendCert_revoke(t *testing.T) {
	rootPath := "pki-root-" + strconv.Itoa(acctest.RandInt())
	intermediatePath := "pki-intermediate-" + strconv.Itoa(acctest.RandInt())

	var store testPKICertStore

	resourceName := "vault_pki_secret_backend_cert.test"
	resource.Test(t, resource.TestCase{
		Providers:    testProviders,
		PreCheck:     func() { testutil.TestAccPreCheck(t) },
		CheckDestroy: testPkiSecretBackendCertDestroy,
		Steps: []resource.TestStep{
			{
				Config: testPkiSecretBackendCertConfig_basic(rootPath, intermediatePath, true, true),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "backend", intermediatePath),
					resource.TestCheckResourceAttr(resourceName, "common_name", "cert.test.my.domain"),
					resource.TestCheckResourceAttr(resourceName, "ttl", "720h"),
					resource.TestCheckResourceAttr(resourceName, "uri_sans.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "uri_sans.0", "spiffe://test.my.domain"),
					resource.TestCheckResourceAttr(resourceName, "uri_sans.0", "spiffe://test.my.domain"),
					resource.TestCheckResourceAttr(resourceName, "revoke", "true"),
					testCapturePKICert(resourceName, &store),
				),
			},
			{
				// remove the cert to test revocation flow (expect revocation)
				Config: testPkiSecretBackendCertConfig_basic(rootPath, intermediatePath, false, false),
				Check: resource.ComposeTestCheckFunc(
					testPKICertRevocation(intermediatePath, &store),
				),
			},
		},
	})
}

func testPkiSecretBackendCertDestroy(s *terraform.State) error {
	client := testProvider.Meta().(*api.Client)

	mounts, err := client.Sys().ListMounts()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "vault_mount" {
			continue
		}
		for path, mount := range mounts {
			path = strings.Trim(path, "/")
			rsPath := strings.Trim(rs.Primary.Attributes["path"], "/")
			if mount.Type == "pki" && path == rsPath {
				return fmt.Errorf("Mount %q still exists", path)
			}
		}
	}
	return nil
}

func testPkiSecretBackendCertConfig_basic(rootPath, intermediatePath string, withCert, revoke bool) string {
	fragments := []string{
		fmt.Sprintf(`
resource "vault_mount" "test-root" {
  path = "%s"
  type = "pki"
  description = "test root"
  default_lease_ttl_seconds = "8640000"
  max_lease_ttl_seconds = "8640000"
}

resource "vault_mount" "test-intermediate" {
  depends_on = [ "vault_mount.test-root" ]
  path = "%s"
  type = "pki"
  description = "test intermediate"
  default_lease_ttl_seconds = "86400"
  max_lease_ttl_seconds = "86400"
}

resource "vault_pki_secret_backend_root_cert" "test" {
  depends_on = [ "vault_mount.test-intermediate" ]
  backend = vault_mount.test-root.path
  type = "internal"
  common_name = "my.domain"
  ttl = "86400"
  format = "pem"
  private_key_format = "der"
  key_type = "rsa"
  key_bits = 4096
  ou = "test"
  organization = "test"
  country = "test"
  locality = "test"
  province = "test"
}

resource "vault_pki_secret_backend_intermediate_cert_request" "test" {
  depends_on = [ "vault_pki_secret_backend_root_cert.test" ]
  backend = vault_mount.test-intermediate.path
  type = "internal"
  common_name = "test.my.domain"
}

resource "vault_pki_secret_backend_root_sign_intermediate" "test" {
  depends_on = [ "vault_pki_secret_backend_intermediate_cert_request.test" ]
  backend = vault_mount.test-root.path
  csr = vault_pki_secret_backend_intermediate_cert_request.test.csr
  common_name = "test.my.domain"
  permitted_dns_domains = [".test.my.domain"]
  ou = "test"
  organization = "test"
  country = "test"
  locality = "test"
  province = "test"
}

resource "vault_pki_secret_backend_intermediate_set_signed" "test" {
  depends_on = [ "vault_pki_secret_backend_root_sign_intermediate.test" ]
  backend = vault_mount.test-intermediate.path
  certificate = vault_pki_secret_backend_root_sign_intermediate.test.certificate
}

resource "vault_pki_secret_backend_role" "test" {
  depends_on = [ "vault_pki_secret_backend_intermediate_set_signed.test" ]
  backend = vault_mount.test-intermediate.path
  name = "test"
  allowed_domains  = ["test.my.domain"]
  allow_subdomains = true
  allowed_uri_sans = ["spiffe://test.my.domain"]
  max_ttl = "3600"
  key_usage = ["DigitalSignature", "KeyAgreement", "KeyEncipherment"]
}
`, rootPath, intermediatePath),
	}

	if withCert {
		fragments = append(fragments, fmt.Sprintf(`
resource "vault_pki_secret_backend_cert" "test" {
  depends_on            = ["vault_pki_secret_backend_role.test"]
  backend               = vault_mount.test-intermediate.path
  name                  = vault_pki_secret_backend_role.test.name
  common_name           = "cert.test.my.domain"
  uri_sans              = ["spiffe://test.my.domain"]
  ttl                   = "720h"
  min_seconds_remaining = 60
  revoke                = %t
}
`, revoke))
	}

	return strings.Join(fragments, "\n")
}

func TestPkiSecretBackendCert_renew(t *testing.T) {
	rootPath := "pki-root-" + strconv.Itoa(acctest.RandInt())

	resourceName := "vault_pki_secret_backend_cert.test"
	resource.Test(t, resource.TestCase{
		Providers:    testProviders,
		PreCheck:     func() { testutil.TestAccPreCheck(t) },
		CheckDestroy: testPkiSecretBackendCertDestroy,
		Steps: []resource.TestStep{
			{
				Config: testPkiSecretBackendCertConfig_renew(rootPath),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "backend", rootPath),
					resource.TestCheckResourceAttr(resourceName, "common_name", "cert.test.my.domain"),
					resource.TestCheckResourceAttr(resourceName, "ttl", "1h"),
					resource.TestCheckResourceAttr(resourceName, "min_seconds_remaining", "3595"),
					resource.TestCheckResourceAttr(resourceName, "revoke", "false"),
					resource.TestCheckResourceAttrSet(resourceName, "expiration"),
				),
			},
			{
				Config:   testPkiSecretBackendCertConfig_renew(rootPath),
				PlanOnly: true,
			},
			{
				Config: testPkiSecretBackendCertConfig_renew(rootPath),
				Check: resource.ComposeTestCheckFunc(
					testPkiSecretBackendCertWaitUntilRenewal(resourceName),
					resource.TestCheckResourceAttr(resourceName, "backend", rootPath),
					resource.TestCheckResourceAttr(resourceName, "common_name", "cert.test.my.domain"),
					resource.TestCheckResourceAttr(resourceName, "ttl", "1h"),
					resource.TestCheckResourceAttr(resourceName, "min_seconds_remaining", "3595"),
					resource.TestCheckResourceAttr(resourceName, "revoke", "false"),
					resource.TestCheckResourceAttrSet(resourceName, "expiration"),
				),
				ExpectNonEmptyPlan: true,
			},
			{
				Config: testPkiSecretBackendCertConfig_renew(rootPath),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "backend", rootPath),
					resource.TestCheckResourceAttr(resourceName, "common_name", "cert.test.my.domain"),
					resource.TestCheckResourceAttr(resourceName, "ttl", "1h"),
					resource.TestCheckResourceAttr(resourceName, "min_seconds_remaining", "3595"),
					resource.TestCheckResourceAttr(resourceName, "revoke", "false"),
					resource.TestCheckResourceAttrSet(resourceName, "expiration"),
				),
			},
		},
	})
}

func testPkiSecretBackendCertConfig_renew(rootPath string) string {
	return fmt.Sprintf(`
resource "vault_mount" "test-root" {
  path = "%s"
  type = "pki"
  description = "test root"
  default_lease_ttl_seconds = "8640000"
  max_lease_ttl_seconds = "8640000"
}

resource "vault_pki_secret_backend_root_cert" "test" {
  depends_on = [ "vault_mount.test-root" ]
  backend = vault_mount.test-root.path
  type = "internal"
  common_name = "my.domain"
  ttl = "86400"
  format = "pem"
  private_key_format = "der"
  key_type = "rsa"
  key_bits = 4096
  ou = "test"
  organization = "test"
  country = "test"
  locality = "test"
  province = "test"
}

resource "vault_pki_secret_backend_role" "test" {
  depends_on = [ "vault_pki_secret_backend_root_cert.test" ]
  backend = vault_mount.test-root.path
  name = "test"
  allowed_domains  = ["test.my.domain"]
  allow_subdomains = true
  max_ttl = "3600"
  key_usage = ["DigitalSignature", "KeyAgreement", "KeyEncipherment"]
}

resource "vault_pki_secret_backend_cert" "test" {
  depends_on = [ "vault_pki_secret_backend_role.test" ]
  backend = vault_mount.test-root.path
  name = vault_pki_secret_backend_role.test.name
  common_name = "cert.test.my.domain"
  ttl = "1h"
  auto_renew = true
  min_seconds_remaining = "3595"
}`, rootPath)
}

func testPkiSecretBackendCertWaitUntilRenewal(n string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[n]
		if !ok {
			return fmt.Errorf("Not found: %s", n)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("No ID is set")
		}

		expiration, err := strconv.Atoi(rs.Primary.Attributes["expiration"])
		if err != nil {
			return fmt.Errorf("Invalid expiration value: %s", err)
		}

		minSecondsRemain, err := strconv.Atoi(rs.Primary.Attributes["min_seconds_remaining"])
		if err != nil {
			return fmt.Errorf("Invalid min_seconds_remaining value: %s", err)
		}

		secondsUntilRenewal := (expiration - (int(time.Now().Unix()) + minSecondsRemain))
		time.Sleep(time.Duration(secondsUntilRenewal+1) * time.Second)

		return nil
	}
}

func testCapturePKICert(resourcePath string, store *testPKICertStore) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "vault_pki_secret_backend_cert" {
				continue
			}

			store.cert = rs.Primary.Attributes["certificate"]
			v, err := strconv.ParseBool(rs.Primary.Attributes["revoke"])
			if err != nil {
				return err
			}
			store.expectRevoked = v
			return nil
		}
		return fmt.Errorf("certificate not found in state")
	}
}

func testPKICertRevocation(path string, store *testPKICertStore) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		if store.cert == "" {
			return fmt.Errorf("certificate in %#v is empty", store)
		}

		addr := testProvider.Meta().(*api.Client).Address()
		url := fmt.Sprintf("%s/v1/%s/crl", addr, path)
		c := cleanhttp.DefaultClient()
		resp, err := c.Get(url)
		if err != nil {
			return err
		}

		if resp.StatusCode > http.StatusAccepted {
			return fmt.Errorf("invalid response, %#v", resp)
		}

		defer resp.Body.Close()

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		crl, err := x509.ParseCRL(body)
		if err != nil {
			return err
		}

		p, _ := pem.Decode([]byte(store.cert))
		cert, err := x509.ParseCertificate(p.Bytes)
		if err != nil {
			return err
		}

		for _, revoked := range crl.TBSCertList.RevokedCertificates {
			if cert.SerialNumber.Cmp(revoked.SerialNumber) == 0 {
				if !store.expectRevoked {
					return fmt.Errorf("cert unexpectedly revoked, serial number %v, revocations %#v",
						cert.SerialNumber, crl.TBSCertList.RevokedCertificates)
				}
				return nil
			}
		}

		if store.expectRevoked {
			return fmt.Errorf("cert not revoked, serial number %v, revocations %#v",
				cert.SerialNumber, crl.TBSCertList.RevokedCertificates)
		}

		return nil
	}
}
