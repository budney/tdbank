// Copyright 2017 Len Budney. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package tdbank provides methods for online banking with TD Bank.
// It uses agouti's Chrome web driver to access the bank's web site.
// For now it only supports scraping account histories.
package tdbank

import (
	"errors"
	"fmt"
	"github.com/araddon/dateparse"
	"github.com/sclevine/agouti"
	"log"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultLoginUrl        = "https://onlinebanking.tdbank.com/"
	AccountBalanceSelector = "table[id=Table2] span, table[id=AccountBalanceSection] span"
	AccountHistorySelector = "table.td-table.td-table-stripe-row.td-table-hover-row.td-table-border-column tbody"
)

var (
	DefaultHandlers = map[string]func(*HistoryRecord, string) error{
		"Date":            (*HistoryRecord).DateFromString,
		"Type":            (*HistoryRecord).TypeFromString,
		"Description":     (*HistoryRecord).DescriptionFromString,
		"Debit":           (*HistoryRecord).DebitFromString,
		"Credit":          (*HistoryRecord).CreditFromString,
		"Account Balance": (*HistoryRecord).BalanceFromString,
	}
)

// A HistoryRecord contains one line from an account history.
// TD Bank includes different fields for credit accounts (i.e.,
// credit cards) and debit accounts (e.g., checking accounts).
// Other methods in this package make reasonable efforts to fill
// in missing fields -- for example, by computing a running balance
// if the account history doesn't show one.
type HistoryRecord struct {
	Index       int
	Date        time.Time
	Type        string
	Description string
	Debit       int64
	Credit      int64
	Balance     int64
}

// A Client represents a virtual web browser. It holds pointers
// to the Chrome web driver and the current page. Most functions
// in this package are implemented as methods of client, because
// they always need a web browser.
type Client struct {
	driver *agouti.WebDriver
	page   *agouti.Page
}

// An Auth holds the URL of the login page, a username and password,
// and answers to the security questions. The Login() method needs
// this information.
type Auth struct {
	LoginUrl          string
	Username          string
	Password          string
	SecurityQuestions map[string]string
}

// Start launches a virtual browser -- i.e., it initializes a Chrome
// web driver and launches Chrome with a blank page. The work is done
// by agouti, which in turn runs chromedriver, so you may want to set
// up chromedriver to your liking. For example you might want to put
// a wrapper (named chromedriver) on your path that launches Chrome in
// headless mode.
func (client *Client) Start() {
	// Ignore repeated attempts to start the driver
	if client.driver != nil {
		return
	}

	client.driver = agouti.ChromeDriver()

	// Start the driver
	if err := client.driver.Start(); err != nil {
		log.Fatalf("Failed to start ChromeDriver: %v", err)
	}

	// Make a new page object

	if page, err := client.driver.NewPage(); err != nil {
		log.Fatalf("Failed to initialize browser: %v", err)
	} else {
		client.page = page
	}

	return
}

// Stop shuts down the web driver and the Chrome process. You
// want to make sure you call Stop every time you call Start,
// or else Chrome processes will continue running after your
// Go program exits. The usual way to do this is:
//
//    var client tdbank.Client;
//
//    client.Start()
//    defer client.Stop()
func (client *Client) Stop() {
	if client.driver == nil {
		log.Print("Ignoring attempt to stop driver that was never started")
		return
	}

	if err := client.driver.Stop(); err != nil {
		log.Fatalf("Failed to stop ChromeDriver: %v", err)
	}
	client.driver = nil

	return
}

// Login connects to the TD Bank login page and logs in with
// the supplied username and password. If it notices any of
// the security questions it was given, then it supplies the
// answer. When this method returns, the browser should be
// at the main accounts page.
func (client *Client) Login(auth Auth) {
	var loginUrl string

	// Use the supplied login URL or the default
	if loginUrl = auth.LoginUrl; loginUrl == "" {
		loginUrl = DefaultLoginUrl
	}

	log.Printf("Going to URL: %s", loginUrl)

	// Start the web driver (using Chrome)
	client.Start()

	if client.page == nil {
		log.Fatalf("Page is nil")
	}

	// Open the main page and switch to the main frame
	err := client.page.Navigate(loginUrl)
	if err != nil {
		log.Fatalf("Failed to open login page %s: %v", loginUrl, err)
	}
	client.page.Find("frame[name=main]").SwitchToFrame()

	// Log in
	selection := client.page.FindByID("txtUser")
	if err = selection.Fill(auth.Username); err != nil {
		log.Printf("Failed to enter username: %v", err)
	}

	selection = client.page.FindByID("txtPassword")
	if err = selection.Fill(auth.Password); err != nil {
		log.Printf("Failed to enter password: %v", err)
	}

	if err = selection.SendKeys("\n"); err != nil {
		log.Printf("Failed to type ENTER: %v", err)
	}

	// Answer the "questions three," if asked:
	selection = client.page.All("td[class=question]").At(0)
	question, err := selection.Text()
	if err == nil {
		answers := auth.SecurityQuestions
		var answer string

		for q, a := range answers {
			if strings.HasPrefix(question, q) {
				answer = a
			}
		}

		if answer == "" {
			log.Fatalf("Couldn't answer question: %s", question)
		}

		selection = client.page.Find("td[class=question] input[name=Newanswer]")
		if err = selection.Fill(answer); err != nil {
			log.Fatalf("Failed to answer security question: %v", err)
		}

		if err = selection.SendKeys("\n"); err != nil {
			log.Printf("Failed to type ENTER: %v", err)
		}
	}

	// Click through the annoying popup. Skip error checking; it might
	// not be there.
	selection = client.page.Find("div[id=continue] a")
	link, _ := selection.Attribute("href")
	if link != "" {
		client.page.Navigate(link)
	}

	return
}

// ViewAccounts takes the browser back to the main accounts page.
func (client *Client) ViewAccounts() error {
	// Find the accounts link
	selection := client.page.FindByLink("Accounts")
	if count, err := selection.Count(); count == 0 || err != nil {
		log.Printf("Unable to find link for accounts tab: %v", err)
		return err
	}

	if err := selection.Click(); err != nil {
		log.Printf("Unable to click accounts tab: %v", err)
		return err
	}

	return nil
}

// ViewAccountHistory clicks on the provided account name, and
// then enters the provided start and end dates to view all
// transactions between those two dates (inclusive). This method
// assumes that the browser is on the main accounts page already,
// so if in doubt you should call ViewAccounts first.
func (client *Client) ViewAccountHistory(account string, start time.Time, end time.Time) error {
	// Find the account link
	selection := client.page.FindByLink(account)
	if count, err := selection.Count(); count == 0 || err != nil {
		log.Printf("Unable to find text field for start date: %v", err)
		return err
	}

	// Click it
	if err := selection.Click(); err != nil {
		log.Printf("Failed to click account link for \""+account+"\": %v", err)
		return err
	}

	// Find start-date field
	selection = client.page.FindByID("Text19")
	if count, err := selection.Count(); count == 0 || err != nil {
		log.Printf("Unable to find text field for start date: %v", err)
		return err
	}

	// Enter the start date
	if err := selection.SendKeys(start.Format("01/02/2006")); err != nil {
		log.Printf("Failed to set start date: %v", err)
		return err
	}
	time.Sleep(500 * time.Millisecond)

	// End date field
	if !end.IsZero() {
		// Find end-date field
		selection = client.page.FindByID("Text20")
		if count, err := selection.Count(); count == 0 || err != nil {
			log.Printf("Unable to find text field for end date: %v", err)
			return err
		}

		// Enter the end date
		if err := selection.SendKeys(end.Format("01/02/2006")); err != nil {
			log.Printf("Failed to set end date: %v", err)
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Find the search button
	selection = client.page.FindByID("btnSearch")
	if count, err := selection.Count(); count == 0 || err != nil {
		log.Printf("Unable to find search button: %v", err)
		return err
	}

	// Clicking the "Show History" button doesn't work in all web drivers
	if err := selection.SendKeys("\n"); err != nil {
		log.Printf("Failed to click the Show History button: %v", err)
		return err
	}

	return nil
}

// ParseAccountBalance assumes that the browser is on the account
// history page for the desired account, and looks within the
// current page for an account balance. It parses the balance as
// a 64-bit integer giving the amount in pennies.
func (client *Client) ParseAccountBalance() (int64, error) {
	// Find the elements that contain the account balance
	selections := client.page.All(AccountBalanceSelector)

	var balance int64
	var err error

	// Check whether we found it
	if count, _ := selections.Count(); count == 0 {
		return 0, errors.New("Unable to find account balance in page")
	} else {
		value, _ := selections.At(count - 1).Text()

		// Convert it to an integer
		if balance, err = parseMoney(value); err != nil {
			return 0, err
		}
	}

	return balance, nil
}

// ParseAccountHistory assumes the browser is on the account history
// page, and looks within the page for a table of transactions. It
// reads each one off the page into a HistoryRecord struct. If some
// struct fields aren't found in the table (such as a running account
// balance), then it makes a reasonable effort to calculate them and
// fill them in anyway.
func (client *Client) ParseAccountHistory() ([]HistoryRecord, error) {
	var history []HistoryRecord
	var fieldNames []string

	// Grab the table with the goodies
	rows := client.page.Find(AccountHistorySelector).All("tr")
	n, _ := rows.Count()

	if n == 0 {
		// There should be at least one row: the header row
		return history, errors.New("No account history found in page")
	}

	// Extract the field names
	header := rows.At(0).All("th, td")
	m, _ := header.Count()

	var balance int64
	hasBalance := false

	// Record the field names
	for i := 0; i < m; i++ {
		text, _ := header.At(i).Text()
		fieldNames = append(fieldNames, text)

		if strings.Contains(text, "Balance") {
			hasBalance = true
		}
	}

	if !hasBalance {
		var err error
		if balance, err = client.ParseAccountBalance(); err != nil {
			log.Printf("Couldn't determine account balance: %v", err)
			return history, err
		}
	}

	// Iterate through the rows
	for i := 1; i < n; i++ {
		row := rows.At(i)
		cells := row.All("th, td")

		record := HistoryRecord{}

		// Construct a history record using field handlers
		for j := 0; j < m; j++ {
			value, _ := cells.At(j).Text()
			field := fieldNames[j]

			method, found := DefaultHandlers[field]
			if !found {
				log.Printf("No handler found for field: %s", field)
				continue
			}

			if err := method(&record, value); err != nil {
				log.Printf("Error reading %s value: %v: %v", field, value, err)
			}
		}

		// If we're reconstructing the balance history, do so now
		if !hasBalance {
			record.Balance = balance
			balance += record.Credit - record.Debit
		}

		// Save the hisory records, but in reverse order
		history = append([]HistoryRecord{record}, history...)
	}

	index := 0
	var date time.Time

	// Once more, this time to set the Index field
	for i := 0; i < len(history); i++ {
		record := &history[i]

		if record.Date.Equal(date) {
			index++
		} else {
			index = 1
			date = record.Date
		}

		record.Index = index
	}

	return history, nil
}

// GetHtml returns the HTML for the current web page
// as a string. This is useful in a pinch for
// debugging.
func (client *Client) GetHtml() (string, error) {
	return client.page.HTML()
}

// PrintHtml prints the HTML for the current web page.
// This is handy if something breaks, and you want to
// inspect the page where something went wrong.
func (client *Client) PrintHtml() {
	if source, err := client.GetHtml(); err != nil {
		log.Fatalf("Can't get HTML: %v", err)
	} else {
		fmt.Println(source)
	}
}

// DateFromString attempts to parse the string as a date and, if
// successful, stores it in the HistoryRecord's Date field.
func (record *HistoryRecord) DateFromString(value string) error {
	value = strings.TrimSpace(value)

	if date, err := dateparse.ParseLocal(value); err != nil {
		return err
	} else {
		record.Date = date
	}

	return nil
}

// DebitFromString attempts to parse the string as a monetary value
// and, if successful, stores it in the HistoryRecord's Debit field.
// The value is parsed as an integer number of pennies.
func (record *HistoryRecord) DebitFromString(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if debit, err := parseMoney(value); err != nil {
		return err
	} else {
		record.Debit = debit
	}

	return nil
}

// CreditFromString attempts to parse the string as a monetary value
// and, if successful, stores it in the HistoryRecord's Credit field.
// The value is parsed as an integer number of pennies.
func (record *HistoryRecord) CreditFromString(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if credit, err := parseMoney(value); err != nil {
		return err
	} else {
		record.Credit = credit
	}

	return nil
}

// BalanceFromString attempts to parse the string as a monetary value
// and, if successful, stores it in the HistoryRecord's Balance field.
// The value is parsed as an integer number of pennies.
func (record *HistoryRecord) BalanceFromString(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if balance, err := parseMoney(value); err != nil {
		return err
	} else {
		record.Balance = balance
	}

	return nil
}

// TypeFromString stores the string in the HistoryRecord's Type field.
// The string first has any leading or trailing whitepace removed.
func (record *HistoryRecord) TypeFromString(value string) error {
	record.Type = strings.TrimSpace(value)
	return nil
}

// DescriptionFromString stores the string in the HistoryRecord's
// Description field. The string first has any leading or trailing
// whitepace removed.
func (record *HistoryRecord) DescriptionFromString(value string) error {
	record.Description = strings.TrimSpace(value)
	return nil
}

// parseMoney attempts to parse a string as an integer number of
// pennies.
func parseMoney(value string) (int64, error) {
	// Strip dollar signs, commas, and periods from the string
	value = strings.Map(func(r rune) rune {
		if strings.IndexRune("$,.", r) < 0 {
			return r
		} else {
			return -1
		}
	}, value)

	return strconv.ParseInt(value, 10, 64)
}
