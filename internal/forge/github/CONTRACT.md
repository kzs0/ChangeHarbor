# GitHub read contract
NewClient(baseURL,token string,httpClient *http.Client,maxBytes int64) *Client.
Client.Change(ctx,owner,repo string,number int) (Change,error) GET /repos/{owner}/{repo}/pulls/{number}. Change fields RepositoryID,SourceRepositoryID,HeadOID,TargetOID string. Require response id/repo IDs, head/base sha and reject mutable/missing revision; no assumption of fixed SHA length. HTTP errors, oversized body, context cancellation must return errors. Never return successful empty/unknown source identity.
TrustedCheck(Check, expectedProducer, expectedOID string) bool. Check fields Name,Producer,OID,Conclusion string. Only exact producer/revision success qualifies. Name alone insufficient.
CreateComment(ctx,owner,repo,number,body) and UpdateComment(ctx,owner,repo,id,body) write PR issue comments and return Comment{ID,Body,AuthorID} from the response; 401/403/404/410/422 are ErrRejected (definitely not written), other failures ambiguous. Non-2xx responses are *StatusError.
