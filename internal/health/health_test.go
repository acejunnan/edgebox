package health

import (
	"testing"
	"time"
)

func TestReportKeepsDomainsIndependent(t *testing.T) {
	now := time.Now()
	report := NewReport(now)
	management := Observation{Domain: DomainManagement, Status: StatusHealthy, ObservedAt: now, ReasonCode: "tunnel_observed"}
	transport := Observation{Domain: DomainTransport, Status: StatusUnhealthy, ObservedAt: now, ReasonCode: "transport_down"}
	if err := report.Set(management); err != nil {
		t.Fatal(err)
	}
	if err := report.Set(transport); err != nil {
		t.Fatal(err)
	}

	gotManagement, _ := report.Get(DomainManagement)
	gotTransport, _ := report.Get(DomainTransport)
	if gotManagement.Status != StatusHealthy || gotTransport.Status != StatusUnhealthy {
		t.Fatalf("management=%s transport=%s", gotManagement.Status, gotTransport.Status)
	}
	if len(report.Snapshot()) != len(Domains()) {
		t.Fatal("report collapsed or omitted health domains")
	}
}

func TestDomainRegistryCannotBeMutatedByCallers(t *testing.T) {
	first := Domains()
	first[0] = Domain("caller_mutation")
	second := Domains()
	if second[0] != DomainSystem {
		t.Fatalf("domain registry was mutated through returned slice: %q", second[0])
	}
	if !validDomain(DomainSystem) || validDomain(Domain("caller_mutation")) {
		t.Fatal("domain validation was affected by caller mutation")
	}
}

func TestReportCopiesDetails(t *testing.T) {
	details := map[string]string{"source": "test"}
	report := NewReport(time.Now())
	observation := Observation{Domain: DomainWAN, Status: StatusHealthy, ObservedAt: time.Now(), ReasonCode: "link_up", Details: details}
	if err := report.Set(observation); err != nil {
		t.Fatal(err)
	}
	details["source"] = "mutated"
	got, _ := report.Get(DomainWAN)
	if got.Details["source"] != "test" {
		t.Fatal("report retained caller-owned details map")
	}
}
