package session

import "testing"

func newSPICEProxyUnderTest(target string) proxyUnderTest { return NewSPICEProxy(target) }

func TestSPICEProxyRejectsSecondClient(t *testing.T) {
	checkProxyRejectsSecondClient(t, newSPICEProxyUnderTest)
}

func TestSPICEProxyStopClosesActivePairs(t *testing.T) {
	checkProxyStopReturns(t, newSPICEProxyUnderTest)
}

func TestSPICEProxyFreesSlotOnDisconnect(t *testing.T) {
	checkProxyFreesSlotOnDisconnect(t, newSPICEProxyUnderTest)
}
