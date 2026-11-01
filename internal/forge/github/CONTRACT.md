# GitHub read contract
NewClient(baseURL,token string,httpClient *http.Client,maxBytes int64) *Client.
Client.Change(ctx,owner,repo string,number int) (Change,error) GET /repos/{owner}/{repo}/pulls/{number}. Change fields RepositoryID,SourceRepositoryID,HeadOID,TargetOID string. Require response id/repo IDs, head/base sha and reject mutable/missing revision; no assumption of fixed SHA length. HTTP errors, oversized body, context cancellation must return errors. Never return successful empty/unknown source identity.
TrustedCheck(Check, expectedProducer, expectedOID string) bool. Check fields Name,Producer,OID,Conclusion string. Only exact producer/revision success qualifies. Name alone insufficient.
