/*
Copyright 2020 Rodrigue Chakode and contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    
    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/smtp"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type SendMailRequest struct {
	from    string
	to      []string
	subject string
	body    string
}

type ContactRequest struct {
	Name          string `json:"name,omitempty"`
	Email         string `json:"email,omitempty"`
	Organization  string `json:"organization,omitempty"`
	Subject       string `json:"subject,omitempty"`
	Message       string `json:"message,omitempty"`
	RequestTarget string `json:"requestType,omitempty"`
}

type ContactResponse struct {
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// NewSendMailRequest creates a new instance to manage send mail
func NewSendMailRequest(from string, to []string, subject string) *SendMailRequest {
	return &SendMailRequest{
		from:    from,
		to:      to,
		subject: subject,
	}
}

// Execute processes the actual email sending
func (m *SendMailRequest) Execute() error {

	mime := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
	from := "From: " + m.from + "\n"
	subject := "Subject: " + m.subject + "\n"
	msg := []byte(from + subject + mime + "\n" + m.body)

	// Connect to the SMTP Server
	smtpServerAddr := viper.GetString("SMTP_SERVER_ADDR")
	smtpServerHost, _, _ := net.SplitHostPort(smtpServerAddr)
	smtpClientAuth := smtp.PlainAuth("",
		viper.GetString("SMTP_CLIENT_USERNAME"),
		viper.GetString("SMTP_CLIENT_PASSWORD"),
		smtpServerHost)

	// TLS config
	tlsconfig := &tls.Config{
		InsecureSkipVerify: viper.GetBool("SMTP_VERITY_CERT"),
		ServerName:         smtpServerHost,
	}

	// Important: call tls.Dial instead of smtp.Dial for smtp servers running on 465.
	// On port 465 ssl connection is required from the very beginning (no starttls)
	conn, err := tls.Dial("tcp", smtpServerAddr, tlsconfig)
	if err != nil {
		return fmt.Errorf("failed initiating smtp connection (%s)", err)
	}

	smtpClient, err := smtp.NewClient(conn, smtpServerHost)
	if err != nil {
		return fmt.Errorf("failed creating the smtp client (%s)", err)
	}
	defer smtpClient.Quit()

	if err = smtpClient.Auth(smtpClientAuth); err != nil {
		return fmt.Errorf("failed authenticating to smtp server (%s)", err)
	}

	// Initialize a mail transaction
	err = smtpClient.Mail(m.from)
	if err != nil {
		return fmt.Errorf("failed issuing MAIL command (%s)", err)
	}

	// Set recipents
	for _, recipient := range m.to {
		err = smtpClient.Rcpt(recipient)
		if err != nil {
			return fmt.Errorf("failed issuing RCPT command (%s)", err)
		}
	}

	smtpWriter, err := smtpClient.Data()
	if err != nil {
		return fmt.Errorf("failed issuing DATA command (%s)", err)
	}

	_, err = smtpWriter.Write([]byte(msg))
	if err != nil {
		return fmt.Errorf("failed sending mail content (%s)", err)
	}

	err = smtpWriter.Close()
	if err != nil {
		return fmt.Errorf("failed close smtp client (%s)", err)
	}

	return nil
}

// ParseTemplate parses template and bing data and process the email sending
func (m *SendMailRequest) ParseTemplate(templateFileName string, data interface{}) error {
	emailTpl, err := template.ParseFiles(templateFileName)
	if err != nil {
		return err
	}
	buf := new(bytes.Buffer)
	err = emailTpl.Execute(buf, data)
	if err != nil {
		return err
	}

	m.body = buf.String()
	return nil
}

// SendMail handles HTTP request to send email
func SendMail(httpResp http.ResponseWriter, httpReq *http.Request) {

	allowedDomains := strings.Split(viper.GetString("ALLOWED_ORIGINS"), ",")
	allowedOrigins := make(map[string]bool)
	for _, domain := range allowedDomains {
		domainTrimmed := strings.TrimSpace(domain)
		allowedOrigins[fmt.Sprintf("http://%s", domainTrimmed)] = true
		allowedOrigins[fmt.Sprintf("https://%s", domainTrimmed)] = true
		allowedOrigins[fmt.Sprintf("http://www.%s", domainTrimmed)] = true
		allowedOrigins[fmt.Sprintf("https://www.%s", domainTrimmed)] = true
	}
	if len(httpReq.Header["Origin"]) == 0 || len(httpReq.Header["Referer"]) == 0 {
		rawHeader, _ := json.Marshal(httpReq.Header)
		log.Infoln("request with unexpected headers", string(rawHeader))
		httpResp.WriteHeader(http.StatusForbidden)
		return
	}

	reqOrigin := httpReq.Header["Origin"][0]
	if _, domainFound := allowedOrigins[reqOrigin]; !domainFound {
		log.Errorln("not allowed origin", reqOrigin)
		httpResp.WriteHeader(http.StatusForbidden)
		return
	}

	httpReq.ParseForm()

	contactRequest := ContactRequest{
		Name:          httpReq.FormValue("name"),
		Email:         strings.TrimSpace(httpReq.FormValue("email")),
		Organization:  httpReq.FormValue("organization"),
		Subject:       httpReq.FormValue("subject"),
		Message:       httpReq.FormValue("message"),
		RequestTarget: httpReq.FormValue("target"),
	}

	var recipients []string
	switch contactRequest.RequestTarget {
	case "demo":
		recipients = []string{contactRequest.Email, viper.GetString("CONTACT_REPLY_BCC_EMAIL")}
	case "contact":
		recipients = []string{viper.GetString("CONTACT_REPLY_BCC_EMAIL")}
	default:
		log.Infoln("not allowed request type:", contactRequest.RequestTarget)
		httpResp.WriteHeader(http.StatusForbidden)
		return
	}

	userData, _ := json.Marshal(contactRequest)
	log.Infoln("New Request:", string(userData))

	templateData := struct {
		Name         string
		Email        string
		Organization string
		Subject      string
		Message      string
		DemoURL      string
	}{
		Name:         contactRequest.Name,
		Email:        contactRequest.Email,
		Organization: contactRequest.Organization,
		Subject:      contactRequest.Subject,
		Message:      contactRequest.Message,
		DemoURL:      viper.GetString("DEMO_URL"),
	}
	contactResponse := ContactResponse{}
	contactEmail := viper.GetString("CONTACT_REPLY_EMAIL")

	sendMailReq := NewSendMailRequest(
		contactEmail,
		recipients,
		contactRequest.Subject,
	)

	replyTplFile := ""
	if contactRequest.RequestTarget == "demo" {
		replyTplFile = viper.GetString("TEMPLATE_DEMO_REQUEST_REPLY");
		if replyTplFile == "" {
			replyTplFile = "./templates/template_reply_demo_request.html"
		}
	} else {
		replyTplFile = viper.GetString("TEMPLATE_CONTACT_REQUEST_REPLY");
		if replyTplFile == "" {
			replyTplFile = "./templates/template_reply_contact_request.html"
		}
	}
	err := sendMailReq.ParseTemplate(replyTplFile, templateData)

	if err == nil {
		err := sendMailReq.Execute()
		if err != nil {
			log.Infof("error: %s", err.Error())
			contactResponse.Status = "error"
			contactResponse.Message = fmt.Sprintf("An internal error occurred, please try later or send us an email at %s.", viper.GetString("CONTACT_REPLY_BCC_EMAIL"))
		} else {
			contactResponse.Status = "success"
			if contactRequest.RequestTarget == "demo" {
				contactResponse.Message = "Thank you, if you supplied a correct email address then an email should have been sent to you."
			} else {
				contactResponse.Message = "Thank you, if you supplied a correct email address then we'll process your request within the next 48 hours."
			}
		}
	} else {
		log.Infof("error: %s", err.Error())
		contactResponse.Status = "error"
		contactResponse.Message = "Invalid request, please review your input and try again."
	}

	refererURL := strings.Split(httpReq.Header["Referer"][0], "?")[0]
	respRawData, _ := json.Marshal(contactResponse)

	httpResp.Header().Set("Location",
		fmt.Sprintf("%s?status=%s&message=%s",
			refererURL,
			contactResponse.Status,
			contactResponse.Message))
	httpResp.WriteHeader(http.StatusSeeOther)
	httpResp.Header().Set("Content-Type", "application/json; charset=UTF-8")
	httpResp.Write(respRawData)
}
