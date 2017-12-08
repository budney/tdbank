// Client to connect to TD bank and scrape transactions as CSV.
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
	DefaultLoginUrl = "https://onlinebanking.tdbank.com/"
    AccountBalanceSelector = "table[id=Table2] span, table[id=AccountBalanceSection] span"
    AccountHistorySelector = "table.td-table.td-table-stripe-row.td-table-hover-row.td-table-border-column tbody"
)

var (
	DefaultHandlers = map[string]func(*HistoryRecord, string) error{
		"Date":            DateFromString,
		"Type":            TypeFromString,
		"Description":     DescriptionFromString,
		"Debit":           DebitFromString,
		"Credit":          CreditFromString,
		"Account Balance": BalanceFromString,
	}
)

// Contains a history record
type HistoryRecord struct {
	Index       int
	Date        time.Time
	Type        string
	Description string
	Debit       int64
	Credit      int64
	Balance     int64
}

// Client connection information
type Client struct {
	driver *agouti.WebDriver
	page   *agouti.Page
}

// Used to pass authorization to a new client
type Auth struct {
	LoginUrl          string
	Username          string
	Password          string
	SecurityQuestions map[string]string
}

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

// This function assumes the client is located on the account list page,
// and opens the account history for the specified dates
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

func (client *Client) ParseAccountBalance() (int64, error) {
    // Find the elements that contain the account balance
    selections := client.page.All(AccountBalanceSelector)

    var balance int64
    var err error

    // Check whether we found it
    if count, _ := selections.Count(); count == 0 {
        return 0, errors.New("Unable to find account balance in page")
    } else {
        value, _ := selections.At(count-1).Text()

        // Convert it to an integer
        if balance, err = parseMoney(value); err != nil {
            return 0, err
        }
    }

    return balance, nil
}

// This function assumes the client is located on the account history page,
// and the history is displayed.
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

func (client *Client) GetHtml() (string, error) {
	return client.page.HTML()
}

func (client *Client) PrintHtml() {
	if source, err := client.GetHtml(); err != nil {
		log.Fatalf("Can't get HTML: %v", err)
	} else {
		fmt.Println(source)
	}
}

func DateFromString(record *HistoryRecord, value string) error {
	value = strings.TrimSpace(value)

	if date, err := dateparse.ParseLocal(value); err != nil {
		return err
	} else {
		record.Date = date
	}

	return nil
}

func DebitFromString(record *HistoryRecord, value string) error {
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

func CreditFromString(record *HistoryRecord, value string) error {
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

func BalanceFromString(record *HistoryRecord, value string) error {
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

func TypeFromString(record *HistoryRecord, value string) error {
	record.Type = strings.TrimSpace(value)
	return nil
}

func DescriptionFromString(record *HistoryRecord, value string) error {
	record.Description = strings.TrimSpace(value)
	return nil
}

// Basically a no-op, supplied for consistent calling conventions
func parseString(value string) (string, error) {
	return value, nil
}

func parseDate(value string) (time.Time, error) {
	return dateparse.ParseLocal(value)
}

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
