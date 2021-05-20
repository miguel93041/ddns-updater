package digitalocean

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/qdm12/ddns-updater/internal/models"
	"github.com/qdm12/ddns-updater/internal/settings/constants"
	"github.com/qdm12/ddns-updater/internal/settings/errors"
	"github.com/qdm12/ddns-updater/internal/settings/headers"
	"github.com/qdm12/ddns-updater/internal/settings/utils"
	"github.com/qdm12/ddns-updater/pkg/publicip/ipversion"
)

type digitalOcean struct {
	domain    string
	host      string
	ipVersion ipversion.IPVersion
	token     string
}

func New(data json.RawMessage, domain, host string, ipVersion ipversion.IPVersion) (d *digitalOcean, err error) {
	extraSettings := struct {
		Token string `json:"token"`
	}{}
	if err := json.Unmarshal(data, &extraSettings); err != nil {
		return nil, err
	}
	d = &digitalOcean{
		domain:    domain,
		host:      host,
		ipVersion: ipVersion,
		token:     extraSettings.Token,
	}
	if err := d.isValid(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *digitalOcean) isValid() error {
	if len(d.token) == 0 {
		return errors.ErrEmptyToken
	}
	return nil
}

func (d *digitalOcean) String() string {
	return utils.ToString(d.domain, d.host, constants.DigitalOcean, d.ipVersion)
}

func (d *digitalOcean) Domain() string {
	return d.domain
}

func (d *digitalOcean) Host() string {
	return d.host
}

func (d *digitalOcean) IPVersion() ipversion.IPVersion {
	return d.ipVersion
}

func (d *digitalOcean) Proxied() bool {
	return false
}

func (d *digitalOcean) BuildDomainName() string {
	return utils.BuildDomainName(d.host, d.domain)
}

func (d *digitalOcean) HTML() models.HTMLRow {
	return models.HTMLRow{
		Domain:    models.HTML(fmt.Sprintf("<a href=\"http://%s\">%s</a>", d.BuildDomainName(), d.BuildDomainName())),
		Host:      models.HTML(d.Host()),
		Provider:  "<a href=\"https://www.digitalocean.com/\">DigitalOcean</a>",
		IPVersion: models.HTML(d.ipVersion.String()),
	}
}

func (d *digitalOcean) setHeaders(request *http.Request) {
	headers.SetUserAgent(request)
	headers.SetContentType(request, "application/json")
	headers.SetAccept(request, "application/json")
	headers.SetAuthBearer(request, d.token)
}

func (d *digitalOcean) getRecordID(ctx context.Context, recordType string, client *http.Client) (
	recordID int, err error) {
	values := url.Values{}
	values.Set("name", d.BuildDomainName())
	values.Set("type", recordType)
	u := url.URL{
		Scheme:   "https",
		Host:     "api.digitalocean.com",
		Path:     "/v2/domains/" + d.domain + "/records",
		RawQuery: values.Encode(),
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	d.setHeaders(request)

	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w: %d: %s",
			errors.ErrBadHTTPStatus, response.StatusCode, utils.BodyToSingleLine(response.Body))
	}

	decoder := json.NewDecoder(response.Body)
	var result struct {
		DomainRecords []struct {
			ID int `json:"id"`
		} `json:"domain_records"`
	}
	if err = decoder.Decode(&result); err != nil {
		return 0, fmt.Errorf("%w: %s", errors.ErrUnmarshalResponse, err)
	}

	if len(result.DomainRecords) == 0 {
		return 0, errors.ErrNotFound
	} else if result.DomainRecords[0].ID == 0 {
		return 0, errors.ErrDomainIDNotFound
	}

	return result.DomainRecords[0].ID, nil
}

func (d *digitalOcean) Update(ctx context.Context, client *http.Client, ip net.IP) (newIP net.IP, err error) {
	recordType := constants.A
	if ip.To4() == nil { // IPv6
		recordType = constants.AAAA
	}

	recordID, err := d.getRecordID(ctx, recordType, client)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errors.ErrGetRecordID, err)
	}

	u := url.URL{
		Scheme: "https",
		Host:   "api.digitalocean.com",
		Path:   fmt.Sprintf("/v2/domains/%s/records/%d", d.domain, recordID),
	}

	buffer := bytes.NewBuffer(nil)
	encoder := json.NewEncoder(buffer)
	requestData := struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Data string `json:"data"`
	}{
		Type: recordType,
		Name: d.host,
		Data: ip.String(),
	}
	if err := encoder.Encode(requestData); err != nil {
		return nil, fmt.Errorf("%w: %s", errors.ErrRequestEncode, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), buffer)
	if err != nil {
		return nil, err
	}
	d.setHeaders(request)

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d: %s",
			errors.ErrBadHTTPStatus, response.StatusCode, utils.BodyToSingleLine(response.Body))
	}

	decoder := json.NewDecoder(response.Body)
	var responseData struct {
		DomainRecord struct {
			Data string `json:"data"`
		} `json:"domain_record"`
	}
	if err := decoder.Decode(&responseData); err != nil {
		return nil, fmt.Errorf("%w: %s", errors.ErrUnmarshalResponse, err)
	}

	newIP = net.ParseIP(responseData.DomainRecord.Data)
	if newIP == nil {
		return nil, fmt.Errorf("IP address received %q is malformed", responseData.DomainRecord.Data)
	} else if !newIP.Equal(ip) {
		return nil, fmt.Errorf("updated IP address %s is not the IP address %s sent for update", newIP, ip)
	}
	return newIP, nil
}