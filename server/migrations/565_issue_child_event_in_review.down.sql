-- Restore the 558 trigger function: only closed statuses record closed/reopened.
CREATE OR REPLACE FUNCTION record_issue_child_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_closed boolean; new_closed boolean; source uuid;
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
  new_closed := NEW.status IN ('done','cancelled') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=NEW.workspace_id AND s.key=NEW.status AND s.category IN ('done','closed'));
  old_closed := OLD.status IN ('done','cancelled') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=OLD.workspace_id AND s.key=OLD.status AND s.category IN ('done','closed'));
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
