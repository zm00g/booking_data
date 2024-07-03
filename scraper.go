package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

type Hotel struct {
	Name            string
	Price           string
	CheckIn         string
	CheckOut        string
	Rating          string
	NumReviews      string
	Address         string
	Amenities       string
	RoomType        string
	Cancellation    string
	Distance        string
	PropertyType    string
	StarRating      string
	BookingURL      string
	Photos          string
	GuestScoreBreak string
	Description     string
}

type Progress struct {
	City  string
	Stage string
	Count int
}

var (
	limiter      = rate.NewLimiter(rate.Every(5*time.Second), 1)
	progressChan = make(chan Progress, 100)
	userAgents   = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/14.0 Safari/605.1.15",
		// Add more user agents here
	}
)

func main() {
	rand.Seed(time.Now().UnixNano())

	cities := []string{
		"Houston", "San Antonio", "Dallas", "Austin", "Fort Worth",
		"El Paso", "Arlington", "Corpus Christi", "Plano", "Laredo",
	}

	if err := scrapeCities(cities); err != nil {
		log.Fatalf("Error scraping cities: %v", err)
	}
	log.Println("Scraping completed successfully")
}

func scrapeCities(cities []string) error {
	eg, ctx := errgroup.WithContext(context.Background())
	sem := make(chan struct{}, 3) // Increase concurrent scraping to 3 cities

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("could not start playwright: %v", err)
	}
	defer pw.Stop()

	for _, city := range cities {
		city := city
		eg.Go(func() error {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return ctx.Err()
			}

			cityCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()

			err := scrapeCity(cityCtx, pw, city)
			if err == context.DeadlineExceeded {
				log.Printf("Scraping %s timed out", city)
			}
			return err
		})
	}

	return eg.Wait()
}

func scrapeCity(ctx context.Context, pw *playwright.Playwright, city string) error {
	checkpoint := func(stage string) {
		log.Printf("[%s] Checkpoint: %s", city, stage)
		progressChan <- Progress{City: city, Stage: stage}
	}

	checkpoint("Starting")
	start := time.Now()
	log.Printf("[%s] Scraping started at: %s", city, start.Format(time.RFC3339))

	checkIn := time.Now().AddDate(0, 0, 1)
	checkOut := checkIn.AddDate(0, 0, 1)
	searchURL := constructBookingURL(city, checkIn, checkOut)

	checkpoint("URL constructed")

	browser, page, err := launchBrowser(pw)
	if err != nil {
		return fmt.Errorf("could not launch browser: %v", err)
	}
	defer browser.Close()

	log.Printf("[%s] Browser context created successfully", city)
	checkpoint("Browser context created")

	var hotels []Hotel

	heartbeat := startHeartbeat(ctx, city)
	defer heartbeat()

	if err := navigateWithRetry(ctx, page, searchURL); err != nil {
		return fmt.Errorf("navigation failed: %v", err)
	}

	checkpoint("Waiting for property cards")
	if err := waitForPropertyCards(page); err != nil {
		return fmt.Errorf("waiting for property cards failed: %v", err)
	}

	if err := captureScreenshot(page, fmt.Sprintf("%s_after_load.png", city)); err != nil {
		return fmt.Errorf("capturing screenshot failed: %v", err)
	}

	checkpoint("Handling initial popups")
	if err := handlePopups(page); err != nil {
		return fmt.Errorf("handling popups failed: %v", err)
	}

	checkpoint("Handling CAPTCHA")
	if err := handleCAPTCHA(page); err != nil {
		return fmt.Errorf("handling CAPTCHA failed: %v", err)
	}

	checkpoint("Loading more results")
	totalProperties, err := loadMoreResults(page)
	if err != nil {
		return fmt.Errorf("loading more results failed: %v", err)
	}

	if err := captureScreenshot(page, fmt.Sprintf("%s_after_load_more.png", city)); err != nil {
		return fmt.Errorf("capturing screenshot failed: %v", err)
	}

	checkpoint("Extracting hotel data")
	if err := extractHotelData(page, &hotels, checkIn, checkOut); err != nil {
		return fmt.Errorf("extracting hotel data failed: %v", err)
	}

	log.Printf("[%s] Extracted %d hotels out of %d total properties", city, len(hotels), totalProperties)

	if len(hotels) < totalProperties {
		log.Printf("[%s] Warning: Not all properties were extracted. Expected %d, got %d", city, totalProperties, len(hotels))
	}

	checkpoint("Exporting to CSV")
	filePath, err := exportToCSV(hotels, city)
	if err != nil {
		return fmt.Errorf("error exporting to CSV for %s: %w", city, err)
	}

	log.Printf("[%s] Scraping completed. Results saved to %s", city, filePath)
	log.Printf("[%s] Scraping ended at: %s. Duration: %v", city, time.Now().Format(time.RFC3339), time.Since(start))

	checkpoint("Completed")
	return nil
}

func launchBrowser(pw *playwright.Playwright) (playwright.Browser, playwright.Page, error) {
	userAgent := userAgents[rand.Intn(len(userAgents))]

	launchOptions := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(false),
		Args: []string{
			"--no-sandbox",
			"--disable-setuid-sandbox",
			"--disable-infobars",
			"--window-size=1920,1080",
			"--ignore-certificate-errors",
			"--ignore-certificate-errors-spki-list",
			"--disable-background-timer-throttling",
			"--disable-backgrounding-occluded-windows",
			"--disable-breakpad",
			"--disable-client-side-phishing-detection",
			"--disable-component-update",
			"--disable-default-apps",
			"--disable-dev-shm-usage",
			"--disable-domain-reliability",
			"--disable-extensions",
			"--disable-features=AudioServiceOutOfProcess",
			"--disable-hang-monitor",
			"--disable-ipc-flooding-protection",
			"--disable-notifications",
			"--disable-offer-store-unmasked-wallet-cards",
			"--disable-popup-blocking",
			"--disable-print-preview",
			"--disable-prompt-on-repost",
			"--disable-renderer-backgrounding",
			"--disable-speech-api",
			"--disable-sync",
			"--hide-scrollbars",
			"--ignore-gpu-blacklist",
			"--metrics-recording-only",
			"--mute-audio",
			"--no-default-browser-check",
			"--no-first-run",
			"--no-pings",
			"--no-zygote",
			"--password-store=basic",
			"--use-gl=swiftshader",
			"--use-mock-keychain",
		},
	}

	browser, err := pw.Chromium.Launch(launchOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("could not launch browser: %v", err)
	}

	context, err := browser.NewContext(playwright.BrowserNewContextOptions{
		UserAgent: playwright.String(userAgent),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("could not create browser context: %v", err)
	}

	script := playwright.Script{
		Content: playwright.String(`
			() => {
				Object.defineProperty(navigator, 'webdriver', {
					get: () => false,
				});
			}
		`),
	}
	err = context.AddInitScript(script)
	if err != nil {
		return nil, nil, fmt.Errorf("could not add init script: %v", err)
	}

	page, err := context.NewPage()
	if err != nil {
		return nil, nil, fmt.Errorf("could not create page: %v", err)
	}

	err = page.SetViewportSize(1920, 1080)
	if err != nil {
		return nil, nil, fmt.Errorf("could not set viewport size: %v", err)
	}

	return browser, page, nil
}

func constructBookingURL(city string, checkIn, checkOut time.Time) string {
	return fmt.Sprintf("https://www.booking.com/searchresults.html?ss=%s&checkin=%s&checkout=%s&group_adults=2&no_rooms=1&group_children=0",
		strings.ReplaceAll(city, " ", "+"),
		checkIn.Format("2006-01-02"),
		checkOut.Format("2006-01-02"))
}

func navigateWithRetry(ctx context.Context, page playwright.Page, url string) error {
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		if err := limiter.Wait(ctx); err != nil {
			return err
		}

		if _, err := page.Goto(url, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateNetworkidle,
			Timeout:   playwright.Float(30000),
		}); err == nil {
			return nil
		}

		log.Printf("Navigation attempt %d failed. Retrying...", i+1)
		time.Sleep(time.Duration(rand.Intn(5)+1) * time.Second)
	}
	return fmt.Errorf("navigation failed after %d attempts", maxRetries)
}

func waitForPropertyCards(page playwright.Page) error {
	_, err := page.WaitForSelector("div[data-testid=\"property-card\"]", playwright.PageWaitForSelectorOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(30000),
	})
	return err
}

func handlePopups(page playwright.Page) error {
	popupSelectors := []string{
		"button[aria-label=\"Dismiss sign-in info.\"]",
		"button[aria-label=\"Close\"]",
		"#onetrust-accept-btn-handler",
	}

	for _, selector := range popupSelectors {
		if err := page.Click(selector, playwright.PageClickOptions{
			Timeout: playwright.Float(5000),
		}); err == nil {
			log.Println("Popup closed")
			time.Sleep(1 * time.Second)
		}
	}
	return nil
}

func handleCAPTCHA(page playwright.Page) error {
	if _, err := page.WaitForSelector("iframe[src*=\"recaptcha\"]", playwright.PageWaitForSelectorOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err == nil {
		log.Println("CAPTCHA detected. Waiting for manual solve...")
		if _, err := page.WaitForSelector("#recaptcha-verify-button", playwright.PageWaitForSelectorOptions{
			State:   playwright.WaitForSelectorStateHidden,
			Timeout: playwright.Float(300000), // 5 minutes timeout for manual solving
		}); err != nil {
			return fmt.Errorf("CAPTCHA solving timed out: %v", err)
		}
		log.Println("CAPTCHA solved")
	}
	return nil
}

func loadMoreResults(page playwright.Page) (int, error) {
	var totalProperties int
	for i := 0; i < 700; i++ { // Set a reasonable upper limit
		if err := limiter.Wait(context.Background()); err != nil {
			return 0, err
		}

		// Check the total number of properties
		totalPropertiesText, err := page.InnerText("h1[data-testid=\"header-title\"]")
		if err == nil {
			parts := strings.Fields(totalPropertiesText)
			if len(parts) > 0 {
				totalProperties, _ = strconv.Atoi(parts[0])
			}
		}

		// Count the number of loaded property cards
		loadedProperties, err := page.QuerySelectorAll("div[data-testid=\"property-card\"]")
		if err != nil {
			return 0, fmt.Errorf("error counting loaded properties: %w", err)
		}

		log.Printf("Loaded %d out of %d properties", len(loadedProperties), totalProperties)

		if len(loadedProperties) >= totalProperties {
			log.Printf("All %d properties loaded", totalProperties)
			return totalProperties, nil
		}

		// Click the "Load more results" button
		if err := page.Click("button[data-testid=\"load-more-results-button\"]", playwright.PageClickOptions{
			Timeout: playwright.Float(5000),
		}); err != nil {
			log.Printf("No more 'Load more results' button found after %d attempts", i+1)
			return len(loadedProperties), nil
		}

		log.Printf("Clicked 'Load more results' button (attempt %d)", i+1)

		// Wait for new results to load
		time.Sleep(time.Duration(rand.Intn(3)+2) * time.Second)

		// Wait for the network to be idle
		if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		}); err != nil {
			log.Printf("Error waiting for network idle: %v", err)
		}
	}

	return totalProperties, fmt.Errorf("reached maximum attempts without loading all properties")
}

func extractHotelData(page playwright.Page, hotels *[]Hotel, checkIn, checkOut time.Time) error {
	cards, err := page.QuerySelectorAll("div[data-testid=\"property-card\"]")
	if err != nil {
		return fmt.Errorf("error querying property cards: %w", err)
	}

	log.Printf("Found %d property cards", len(cards))

	for _, card := range cards {
		hotel := Hotel{
			CheckIn:  checkIn.Format("2006-01-02"),
			CheckOut: checkOut.Format("2006-01-02"),
		}

		// Helper function to safely get text content
		getTextContent := func(selector string) string {
			element, err := card.QuerySelector(selector)
			if err != nil || element == nil {
				return "N/A"
			}
			text, err := element.TextContent()
			if err != nil {
				return "N/A"
			}
			return strings.TrimSpace(text)
		}

		hotel.Name = getTextContent("div[data-testid=\"title\"]")
		hotel.Price = getTextContent("span[data-testid=\"price-and-discounted-price\"]")
		hotel.Rating = getTextContent("div[data-testid=\"review-score\"]")
		hotel.NumReviews = getTextContent("div[data-testid=\"review-score\"] ~ div")
		hotel.Address = getTextContent("span[data-testid=\"address\"]")
		hotel.RoomType = getTextContent("span[data-testid=\"room-info\"]")
		hotel.Cancellation = getTextContent("span[data-testid=\"cancellation-policy\"]")
		hotel.Distance = getTextContent("span[data-testid=\"distance\"]")
		hotel.PropertyType = getTextContent("span[data-testid=\"property-type-badge\"]")
		hotel.StarRating = getTextContent("div[data-testid=\"rating-stars\"]")
		hotel.GuestScoreBreak = getTextContent("div[data-testid=\"review-score-breakdown\"]")
		hotel.Description = getTextContent("div[data-testid=\"property-card-description\"]")

		// Get booking URL
		if urlElement, err := card.QuerySelector("a[data-testid=\"title-link\"]"); err == nil && urlElement != nil {
			hotel.BookingURL, _ = urlElement.GetAttribute("href")
		}

		// Get amenities
		amenities, err := card.QuerySelectorAll("div[data-testid=\"facility-badge\"]")
		if err == nil {
			var amenityTexts []string
			for _, amenity := range amenities {
				text, _ := amenity.TextContent()
				amenityTexts = append(amenityTexts, strings.TrimSpace(text))
			}
			hotel.Amenities = strings.Join(amenityTexts, ", ")
		}

		// Get photos
		photos, err := card.QuerySelectorAll("img[data-testid=\"image\"]")
		if err == nil {
			var photoURLs []string
			for _, photo := range photos {
				src, _ := photo.GetAttribute("src")
				photoURLs = append(photoURLs, src)
			}
			hotel.Photos = strings.Join(photoURLs, ", ")
		}

		*hotels = append(*hotels, hotel)
	}

	log.Printf("Extracted %d hotel records", len(*hotels))
	return nil
}

func exportToCSV(hotels []Hotel, city string) (string, error) {
	currentDate := time.Now().Format("2006-01-02")
	dataDir := filepath.Join("data", currentDate)
	if err := os.MkdirAll(dataDir, os.ModePerm); err != nil {
		return "", fmt.Errorf("could not create data directory: %w", err)
	}

	timestamp := time.Now().Format("15-04-05")
	filename := fmt.Sprintf("%s_hotels_%s.csv", strings.ReplaceAll(city, " ", "_"), timestamp)
	filePath := filepath.Join(dataDir, filename)

	file, err := os.Create(filePath)
	if err != nil {
		return "", fmt.Errorf("could not create file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{"Name", "Price", "CheckIn", "CheckOut", "Rating", "NumReviews", "Address", "Amenities", "RoomType", "Cancellation", "Distance", "PropertyType", "StarRating", "BookingURL", "Photos", "GuestScoreBreak", "Description"}
	if err := writer.Write(header); err != nil {
		return "", fmt.Errorf("error writing header to CSV: %w", err)
	}

	for _, hotel := range hotels {
		row := []string{
			hotel.Name, hotel.Price, hotel.CheckIn, hotel.CheckOut, hotel.Rating, hotel.NumReviews,
			hotel.Address, hotel.Amenities, hotel.RoomType, hotel.Cancellation, hotel.Distance,
			hotel.PropertyType, hotel.StarRating, hotel.BookingURL, hotel.Photos, hotel.GuestScoreBreak,
			hotel.Description,
		}
		if err := writer.Write(row); err != nil {
			return "", fmt.Errorf("error writing row to CSV: %w", err)
		}
	}

	return filePath, nil
}

func startHeartbeat(ctx context.Context, city string) func() {
	ticker := time.NewTicker(30 * time.Second)
	done := make(chan bool)
	go func() {
		for {
			select {
			case <-ticker.C:
				log.Printf("[%s] Still scraping...", city)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		ticker.Stop()
		done <- true
	}
}

func captureScreenshot(page playwright.Page, filename string) error {
	currentDate := time.Now().Format("2006-01-02")
	timestampedDir := time.Now().Format("15-04-05")
	screenshotDir := filepath.Join(".", "screenshots", currentDate, timestampedDir)
	if err := os.MkdirAll(screenshotDir, os.ModePerm); err != nil {
		return fmt.Errorf("could not create screenshot directory: %w", err)
	}

	filePath := filepath.Join(screenshotDir, filename)
	_, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(filePath),
		FullPage: playwright.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("could not capture screenshot: %w", err)
	}

	log.Printf("Screenshot saved: %s", filePath)
	return nil
}




