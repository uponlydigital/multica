-- A workspace can count a sub-issue in In Review as delivered for the
-- child_done system rule (workspace setting system_wakeup_child_done_in_review,
-- off by default). Agents finish at in_review and done stays human, so without
-- it an all-agent chain never wakes the parent (#3597). With the setting on,
-- entering or leaving in_review records the same closed/reopened sub-issue
-- events a closed status does, so the parent's rules are evaluated right away.
-- Rules still read the stored statuses: only the system rule counts in_review;
-- people's --until-children-done conditions keep waiting for closed.
CREATE OR REPLACE FUNCTION record_issue_child_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_closed boolean; new_closed boolean; in_review_counts boolean; source uuid;
BEGIN
 source := NULLIF(current_setting('multica.source_task_id',true),'')::uuid;
 IF TG_OP='INSERT' THEN
  INSERT INTO issue_child_event(workspace_id,parent_id,child_id,kind,source_task_id) VALUES(NEW.workspace_id,NEW.parent_issue_id,NEW.id,'attached',source);
  RETURN NULL;
 END IF;
 IF OLD.parent_issue_id IS DISTINCT FROM NEW.parent_issue_id THEN
  IF OLD.parent_issue_id IS NOT NULL THEN
   INSERT INTO issue_child_event(workspace_id,parent_id,child_id,kind,source_task_id) VALUES(OLD.workspace_id,OLD.parent_issue_id,OLD.id,'detached',source);
  END IF;
  IF NEW.parent_issue_id IS NOT NULL THEN
   INSERT INTO issue_child_event(workspace_id,parent_id,child_id,kind,source_task_id) VALUES(NEW.workspace_id,NEW.parent_issue_id,NEW.id,'attached',source);
  END IF;
  RETURN NULL;
 END IF;
 IF OLD.status IS DISTINCT FROM NEW.status THEN
  in_review_counts := 'in_review' IN (OLD.status, NEW.status)
   AND EXISTS(SELECT 1 FROM workspace w WHERE w.id=NEW.workspace_id AND w.settings->'system_wakeup_child_done_in_review'='true'::jsonb);
  new_closed := NEW.status IN ('done','cancelled') OR (in_review_counts AND NEW.status='in_review') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=NEW.workspace_id AND s.key=NEW.status AND s.category IN ('done','closed'));
  old_closed := OLD.status IN ('done','cancelled') OR (in_review_counts AND OLD.status='in_review') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=OLD.workspace_id AND s.key=OLD.status AND s.category IN ('done','closed'));
  IF new_closed AND NOT old_closed THEN
   INSERT INTO issue_child_event(workspace_id,parent_id,child_id,kind,source_task_id) VALUES(NEW.workspace_id,NEW.parent_issue_id,NEW.id,'closed',source);
  ELSIF old_closed AND NOT new_closed THEN
   INSERT INTO issue_child_event(workspace_id,parent_id,child_id,kind,source_task_id) VALUES(NEW.workspace_id,NEW.parent_issue_id,NEW.id,'reopened',source);
  END IF;
 END IF;
 IF OLD.stage IS DISTINCT FROM NEW.stage THEN
  INSERT INTO issue_child_event(workspace_id,parent_id,child_id,kind,source_task_id) VALUES(NEW.workspace_id,NEW.parent_issue_id,NEW.id,'restaged',source);
 END IF;
 RETURN NULL;
END $$;
