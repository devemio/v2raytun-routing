.PHONY: run
run:
	@go run . domains.txt

.PHONY: dlc
dlc:
	@wget https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat