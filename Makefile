.PHONY: run build update-disposable clean

run:
	go run main.go

build: update-disposable
	go build -o allismail .

update-disposable:
	@echo "Fetching latest disposable domains list..."
	@curl -s -o disposable_domains.txt \
		https://raw.githubusercontent.com/disposable-email-domains/disposable-email-domains/master/disposable_email_blocklist.conf
	@echo "Done. $$(wc -l < disposable_domains.txt) domains loaded."

clean:
	rm -f allismail
